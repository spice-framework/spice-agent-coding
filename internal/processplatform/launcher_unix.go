//go:build linux || darwin

package processplatform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/spice-framework/spice-agent-coding/internal/processcontainment"
	agentprocess "github.com/spice-framework/spice-agent/process"
	"golang.org/x/sys/unix"
)

const unixContainmentPollInterval = 5 * time.Millisecond

// Unix has no portable unprivileged Job Object. This adapter combines a new
// process group with PID-plus-birth-identity discovery. It catches ordinary
// descendants and descendants observed before they detach, never signals a
// reused PID/PGID, and fails containment proof if process-table inspection
// fails. An uncooperative process can still fork, detach, and disappear between
// snapshots on Linux or macOS; callers requiring a sandbox need a stronger
// external policy boundary.
type unixProcess struct {
	command  *exec.Cmd
	groupID  int
	done     chan struct{}
	joined   chan struct{}
	snapshot func() ([]processcontainment.Record, error)

	discoveryMu sync.Mutex
	stateMu     sync.Mutex
	signalMu    sync.Mutex
	joinOnce    sync.Once
	root        processcontainment.Identity
	children    map[int]processcontainment.Identity
	rootDone    bool
	outcome     agentprocess.Outcome
	resultErr   error
	cleanupErr  error
	stopSent    bool
	killSent    bool
}

func startPlatformProcess(
	_ context.Context,
	spec agentprocess.Spec,
	registrar ChildRegistrar,
) (agentprocess.Process, error) {
	return startUnixProcess(spec, registrar, processcontainment.Snapshot)
}

func startUnixProcess(
	spec agentprocess.Spec,
	registrar ChildRegistrar,
	snapshot func() ([]processcontainment.Record, error),
) (agentprocess.Process, error) {
	// #nosec G204 -- Process Spec construction validates an absolute executable;
	// arguments are the caller-authorized coding-tool invocation and no shell is used.
	command := exec.Command(spec.Executable(), spec.Arguments()...) //nolint:noctx // Launch context bounds Start, not the owned process lifetime.
	command.Dir = spec.WorkingDirectory()
	environment := spec.Environment()
	if environment == nil {
		environment = []string{}
	}
	command.Env = environment
	command.Stdin = spec.Stdin()
	command.Stdout = spec.Stdout()
	command.Stderr = spec.Stderr()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return nil, err
	}
	owned := &unixProcess{
		command: command, groupID: command.Process.Pid,
		done: make(chan struct{}), joined: make(chan struct{}),
		snapshot: snapshot,
		children: make(map[int]processcontainment.Identity),
	}
	registrationErr := registrar.Register(command.Process)
	anchorErr := owned.refreshOwnership()
	owned.stateMu.Lock()
	anchored := !owned.root.IsZero()
	if anchorErr == nil && !anchored {
		anchorErr = errors.New("root process identity could not be anchored")
	}
	if anchorErr != nil {
		owned.cleanupErr = errors.Join(owned.cleanupErr, anchorErr)
	}
	owned.stateMu.Unlock()
	var abortErr error
	if anchorErr != nil {
		// The directly started child is still safe to address before Wait reaps it,
		// even though process-table inspection could not establish a reusable-PID
		// identity. Abort the failed launch instead of returning a live candidate
		// that no identity-gated cleanup operation can stop.
		abortErr = owned.signalUnanchoredRoot(os.Kill)
		owned.stateMu.Lock()
		owned.cleanupErr = errors.Join(owned.cleanupErr, abortErr)
		owned.stateMu.Unlock()
	}
	go owned.reap()
	if anchorErr != nil {
		return owned, errors.Join(anchorErr, abortErr, registrationErr)
	}
	go owned.trackOwnership()
	if registrationErr != nil {
		return owned, registrationErr
	}
	return owned, nil
}

func (owned *unixProcess) reap() {
	waitErr := owned.command.Wait()
	outcome, resultErr, cleanupErr := unixOutcome(owned.command.ProcessState, waitErr)
	owned.stateMu.Lock()
	owned.rootDone = true
	owned.outcome = outcome
	owned.resultErr = resultErr
	owned.cleanupErr = errors.Join(owned.cleanupErr, cleanupErr)
	anchored := !owned.root.IsZero()
	owned.stateMu.Unlock()
	close(owned.done)
	if !anchored {
		owned.finishJoin()
	}
}

func unixOutcome(state *os.ProcessState, waitErr error) (agentprocess.Outcome, error, error) {
	if state == nil {
		if waitErr == nil {
			waitErr = errors.New("root process produced no outcome")
		}
		return agentprocess.Outcome{}, agentprocess.NewFailure(agentprocess.OperationResult, waitErr), nil
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok {
		return agentprocess.NewUnknownOutcome(), nil, nonExitWaitFailure(waitErr)
	}
	if status.Signaled() {
		return agentprocess.NewSignaledOutcome(), nil, nonExitWaitFailure(waitErr)
	}
	outcome, err := agentprocess.NewExitedOutcome(int64(status.ExitStatus()))
	if err != nil {
		return agentprocess.Outcome{}, agentprocess.NewFailure(agentprocess.OperationResult, err), nil
	}
	return outcome, nil, nonExitWaitFailure(waitErr)
}

func nonExitWaitFailure(waitErr error) error {
	if waitErr == nil {
		return nil
	}
	if exitFailure, ok := errors.AsType[*exec.ExitError](waitErr); ok && exitFailure != nil {
		return nil //nolint:nilerr // Exit status is the root Outcome, not a containment cleanup failure.
	}
	return waitErr
}

func (owned *unixProcess) trackOwnership() {
	ticker := time.NewTicker(unixContainmentPollInterval)
	defer ticker.Stop()
	for {
		if err := owned.refreshOwnership(); err != nil {
			owned.stateMu.Lock()
			owned.cleanupErr = errors.Join(owned.cleanupErr, err)
			rootDone := owned.rootDone
			owned.stateMu.Unlock()
			if rootDone {
				owned.finishJoin()
				return
			}
		}
		owned.stateMu.Lock()
		complete := owned.rootDone && len(owned.children) == 0
		owned.stateMu.Unlock()
		if complete {
			owned.finishJoin()
			return
		}
		<-ticker.C
	}
}

func (owned *unixProcess) refreshOwnership() error {
	owned.discoveryMu.Lock()
	defer owned.discoveryMu.Unlock()
	records, err := owned.snapshot()
	if err != nil {
		return err
	}
	byPID := indexProcessRecords(records)
	owned.stateMu.Lock()
	defer owned.stateMu.Unlock()
	owned.discoverLocked(byPID)
	return nil
}

func (owned *unixProcess) discoverLocked(processes map[int]processcontainment.Record) {
	root, rootExists := processes[owned.groupID]
	if owned.root.IsZero() && rootExists && !owned.rootDone && root.GroupID == owned.groupID {
		owned.root = root.Identity
	}
	known := make(map[int]struct{}, len(owned.children)+1)
	if rootExists && root.Identity == owned.root {
		known[owned.groupID] = struct{}{}
	}
	for pid, identity := range owned.children {
		if record, exists := processes[pid]; exists && record.Identity == identity {
			known[pid] = struct{}{}
		} else {
			delete(owned.children, pid)
		}
	}
	for changed := true; changed; {
		changed = false
		for _, record := range processes {
			if _, exists := known[record.PID]; exists {
				continue
			}
			if _, parentOwned := known[record.ParentID]; !parentOwned {
				continue
			}
			owned.children[record.PID] = record.Identity
			known[record.PID] = struct{}{}
			changed = true
		}
	}
}

func indexProcessRecords(records []processcontainment.Record) map[int]processcontainment.Record {
	result := make(map[int]processcontainment.Record, len(records))
	for _, record := range records {
		result[record.PID] = record
	}
	return result
}

func (owned *unixProcess) finishJoin() { owned.joinOnce.Do(func() { close(owned.joined) }) }

func (owned *unixProcess) Done() <-chan struct{} {
	if owned == nil {
		return nil
	}
	return owned.done
}

func (owned *unixProcess) Result() (agentprocess.Outcome, error) {
	if owned == nil || owned.done == nil {
		return agentprocess.Outcome{}, agentprocess.NewFailure(agentprocess.OperationResult, errors.New("owned process is unavailable"))
	}
	select {
	case <-owned.done:
		owned.stateMu.Lock()
		defer owned.stateMu.Unlock()
		return owned.outcome, owned.resultErr
	default:
		return agentprocess.Outcome{}, agentprocess.NewFailure(agentprocess.OperationResult, errors.New("root process is still running"))
	}
}

func (owned *unixProcess) RequestStop(ctx context.Context) error {
	if owned == nil {
		return agentprocess.NewFailure(agentprocess.OperationRequestStop, errors.New("owned process is unavailable"))
	}
	return owned.signal(ctx, unix.SIGTERM, agentprocess.OperationRequestStop, &owned.stopSent)
}

func (owned *unixProcess) ForceKill(ctx context.Context) error {
	if owned == nil {
		return agentprocess.NewFailure(agentprocess.OperationForceKill, errors.New("owned process is unavailable"))
	}
	return owned.signal(ctx, unix.SIGKILL, agentprocess.OperationForceKill, &owned.killSent)
}

func (owned *unixProcess) signal(
	ctx context.Context,
	signal unix.Signal,
	operation agentprocess.Operation,
	succeeded *bool,
) error {
	if err := operationContext(ctx, operation); err != nil {
		return err
	}
	owned.signalMu.Lock()
	defer owned.signalMu.Unlock()
	if *succeeded || channelClosed(owned.joined) {
		*succeeded = true
		return nil
	}
	if err := owned.signalOwned(signal); err != nil {
		return agentprocess.NewFailure(operation, err)
	}
	*succeeded = true
	return nil
}

func (owned *unixProcess) signalOwned(signal unix.Signal) error {
	owned.stateMu.Lock()
	anchored, rootDone := !owned.root.IsZero(), owned.rootDone
	owned.stateMu.Unlock()
	if !anchored {
		if rootDone {
			return nil
		}
		return owned.signalUnanchoredRoot(signal)
	}
	owned.discoveryMu.Lock()
	defer owned.discoveryMu.Unlock()
	records, err := owned.snapshot()
	if err != nil {
		return err
	}
	processes := indexProcessRecords(records)
	owned.stateMu.Lock()
	owned.discoverLocked(processes)
	rootIdentity := owned.root
	children := make(map[int]processcontainment.Identity, len(owned.children))
	maps.Copy(children, owned.children)
	owned.stateMu.Unlock()

	return errors.Join(
		signalUnixOwnedGroup(signal, owned.groupID, rootIdentity, children, processes),
		signalUnixEscapedChildren(signal, owned.groupID, children, processes),
	)
}

func (owned *unixProcess) signalUnanchoredRoot(signal os.Signal) error {
	if owned == nil || owned.command == nil || owned.command.Process == nil {
		return errors.New("unanchored root process is unavailable")
	}
	err := owned.command.Process.Signal(signal)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func signalUnixOwnedGroup(
	signal unix.Signal,
	groupID int,
	rootIdentity processcontainment.Identity,
	children map[int]processcontainment.Identity,
	processes map[int]processcontainment.Record,
) error {
	groupOwned := unixGroupIsOwned(groupID, rootIdentity, children, processes)
	if !groupOwned {
		return nil
	}
	if err := unix.Kill(-groupID, signal); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	return nil
}

func unixGroupIsOwned(
	groupID int,
	rootIdentity processcontainment.Identity,
	children map[int]processcontainment.Identity,
	processes map[int]processcontainment.Record,
) bool {
	if root, exists := processes[groupID]; exists &&
		root.Identity == rootIdentity && root.GroupID == groupID {
		return true
	}
	for pid, identity := range children {
		record, exists := processes[pid]
		if exists && record.Identity == identity && record.GroupID == groupID {
			return true
		}
	}
	return false
}

func signalUnixEscapedChildren(
	signal unix.Signal,
	groupID int,
	children map[int]processcontainment.Identity,
	processes map[int]processcontainment.Record,
) error {
	var failures []error
	for pid, identity := range children {
		record, exists := processes[pid]
		if !exists || record.Identity != identity || record.GroupID == groupID {
			continue
		}
		if killErr := unix.Kill(pid, signal); killErr != nil && !errors.Is(killErr, unix.ESRCH) {
			failures = append(failures, killErr)
		}
	}
	return errors.Join(failures...)
}

func (owned *unixProcess) Wait(ctx context.Context) error {
	if err := operationContext(ctx, agentprocess.OperationWait); err != nil {
		return err
	}
	if owned == nil || owned.joined == nil {
		return agentprocess.NewFailure(agentprocess.OperationWait, errors.New("owned process is unavailable"))
	}
	select {
	case <-owned.joined:
		owned.stateMu.Lock()
		defer owned.stateMu.Unlock()
		return terminalContainmentFailure(owned.cleanupErr)
	case <-ctx.Done():
		return agentprocess.NewFailure(agentprocess.OperationWait, context.Cause(ctx))
	}
}

func channelClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func (*unixProcess) String() string         { return "processplatform.Process([REDACTED])" }
func (owned *unixProcess) GoString() string { return owned.String() }
func (*unixProcess) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "processplatform.Process([REDACTED])") //nolint:errcheck // fmt.Formatter cannot return an error.
}
func (owned *unixProcess) LogValue() slog.Value { return slog.StringValue(owned.String()) }

var _ agentprocess.Process = (*unixProcess)(nil)
