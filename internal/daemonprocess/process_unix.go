//go:build linux || darwin

package daemonprocess

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	descendantRegistryEnvironment = "SPICE_AGENT_DESCENDANT_REGISTRY_FD"
	descendantRegistryFD          = 3
	descendantRegistrationBytes   = 8
	descendantDiscoveryInterval   = 100 * time.Millisecond
	descendantAdoptionTimeout     = 2 * time.Second
	descendantCleanupTimeout      = 5 * time.Second
)

// Unix does not provide a portable, unprivileged equivalent of a Windows Job
// Object. Setpgid contains normal descendants, while the identity-checked
// registry and process-table discovery retain descendants that create another
// process group. A daemon must adopt the inherited root registry before child
// beans start and use DescendantRegistry.Start for every child that can detach.
//
// This is deliberately not described as universal kernel containment. On both
// Linux and macOS an unregistered process can fork, reparent, and disappear
// from the ancestry table between discovery passes. A future Linux-only cgroup
// implementation can close that gap when a delegated cgroup is available;
// macOS has no corresponding unprivileged primitive.
type unixLaunchedProcess struct {
	command   *exec.Cmd
	stdin     io.WriteCloser
	registry  *os.File
	stderr    *os.File
	rootPID   int
	waitDelay time.Duration

	operation  sync.Mutex
	discovery  sync.Mutex
	state      sync.Mutex
	root       processIdentity
	children   map[int]processIdentity
	history    []error
	closed     bool
	rootDone   bool
	rootOpened bool

	stopTrack   chan struct{}
	trackerDone chan struct{}
	serverDone  chan struct{}
	stderrDone  chan struct{}
	closeOnce   sync.Once
	closeResult error
}

func startProcess(spec processSpec) (launchedProcess, error) {
	registryFDs, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("create managed daemon descendant registry: %w", err)
	}
	unix.CloseOnExec(registryFDs[0])
	unix.CloseOnExec(registryFDs[1])
	registry := os.NewFile(uintptr(registryFDs[0]), "managed-daemon-descendant-registry")
	childRegistry := os.NewFile(uintptr(registryFDs[1]), "managed-daemon-descendant-registration")
	if registry == nil || childRegistry == nil {
		closeRegistryErr := closeFiles(registry, childRegistry)
		return nil, errors.Join(errors.New("create managed daemon descendant registry files"), closeRegistryErr)
	}

	// The process outlives its launch context and is canceled through the
	// platform containment boundary, not by killing only its root PID.
	command := exec.Command(spec.executable, spec.argument) //nolint:noctx // #nosec G204 -- validated sibling and fixed argument.
	command.Dir = spec.directory
	command.Env = withDescendantRegistry(spec.environment)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.ExtraFiles = []*os.File{childRegistry}
	discard, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, errors.Join(err, closeFiles(registry, childRegistry))
	}
	command.Stdout = discard
	stderr, childStderr, err := os.Pipe()
	if err != nil {
		return nil, errors.Join(err, closeFiles(registry, childRegistry, discard))
	}
	command.Stderr = childStderr
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, errors.Join(err, closeFiles(registry, childRegistry, discard, stderr, childStderr))
	}
	if err = command.Start(); err != nil {
		return nil, errors.Join(err, closeFiles(stdin, registry, childRegistry, discard, stderr, childStderr))
	}

	process := &unixLaunchedProcess{
		command: command, stdin: stdin, registry: registry, stderr: stderr,
		rootPID: command.Process.Pid, waitDelay: spec.waitDelay,
		children: make(map[int]processIdentity), stopTrack: make(chan struct{}),
		trackerDone: make(chan struct{}), serverDone: make(chan struct{}), stderrDone: make(chan struct{}),
	}
	process.recordHistory(errors.Join(childRegistry.Close(), childStderr.Close(), discard.Close()))
	go process.drainStderr(spec.stderr)
	go process.serveRegistrations()
	go process.trackDescendants()
	if err = process.anchorRoot(); err != nil {
		killErr := unix.Kill(-process.rootPID, unix.SIGKILL)
		if errors.Is(killErr, unix.ESRCH) {
			killErr = nil
		}
		process.recordHistory(errors.Join(err, killErr))
		return process, err
	}
	return process, nil
}

func (process *unixLaunchedProcess) anchorRoot() error {
	if err := process.refreshDescendants(); err != nil {
		return err
	}
	process.state.Lock()
	defer process.state.Unlock()
	if process.root.isZero() {
		return errors.New("managed daemon root identity could not be anchored")
	}
	return nil
}

func (process *unixLaunchedProcess) Wait() error {
	if process == nil || process.command == nil {
		return errors.New("managed daemon process is invalid")
	}
	err := process.command.Wait()
	process.state.Lock()
	process.rootDone = true
	process.state.Unlock()
	return err
}

func (process *unixLaunchedProcess) drainStderr(destination io.Writer) {
	defer close(process.stderrDone)
	_, err := io.Copy(destination, process.stderr)
	if err != nil && !errors.Is(err, os.ErrClosed) {
		process.recordHistory(fmt.Errorf("drain managed daemon protected stderr: %w", err))
	}
}

func (process *unixLaunchedProcess) CloseInput() error {
	if process == nil || process.stdin == nil {
		return errors.New("managed daemon control pipe is invalid")
	}
	err := process.stdin.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func (process *unixLaunchedProcess) Terminate() error { return process.signalAll(unix.SIGTERM) }
func (process *unixLaunchedProcess) Kill() error      { return process.signalAll(unix.SIGKILL) }

func (process *unixLaunchedProcess) signalAll(signal unix.Signal) error {
	if process == nil {
		return errors.New("managed daemon process containment is invalid")
	}
	process.operation.Lock()
	defer process.operation.Unlock()
	process.state.Lock()
	closed := process.closed
	process.state.Unlock()
	if closed {
		return nil
	}
	err := errors.Join(process.refreshDescendants(), process.signalOwned(signal))
	process.recordHistory(err)
	return err
}

func (process *unixLaunchedProcess) Close() error {
	if process == nil {
		return nil
	}
	process.closeOnce.Do(func() {
		process.operation.Lock()
		defer process.operation.Unlock()

		// Wait has reaped the root before Close is called. Stop both discovery
		// paths, take one final ancestry snapshot, then kill every identity that
		// remains owned. Closing the registry unblocks its server even if a child
		// incorrectly inherited the descriptor.
		close(process.stopTrack)
		registryShutdownErr := unix.Shutdown(int(process.registry.Fd()), unix.SHUT_RDWR)
		if errors.Is(registryShutdownErr, unix.ENOTCONN) || errors.Is(registryShutdownErr, unix.EINVAL) {
			registryShutdownErr = nil
		}
		registryCloseErr := process.registry.Close()
		<-process.trackerDone
		<-process.serverDone
		finalErr := process.killAndWaitOwned()
		stderrErr := process.waitForStderrDrain()
		if errors.Is(registryCloseErr, os.ErrClosed) {
			registryCloseErr = nil
		}
		process.state.Lock()
		rootOpened := process.rootOpened
		process.state.Unlock()
		var rootOpenErr error
		if !rootOpened {
			rootOpenErr = errors.New("managed daemon did not open its descendant registry")
		}
		process.recordHistory(errors.Join(
			finalErr, registryShutdownErr, registryCloseErr, stderrErr, rootOpenErr, process.CloseInput(),
		))
		process.state.Lock()
		process.closed = true
		process.closeResult = errors.Join(process.history...)
		process.state.Unlock()
	})
	return process.closeResult
}

func (process *unixLaunchedProcess) waitForStderrDrain() error {
	timer := time.NewTimer(process.waitDelay)
	defer timer.Stop()
	select {
	case <-process.stderrDone:
		return process.stderr.Close()
	case <-timer.C:
		closeErr := process.stderr.Close()
		<-process.stderrDone
		// Final signaling has conclusively removed every identity known to the
		// containment boundary. Closing a still-inherited diagnostic descriptor
		// is bounded resource cleanup, not an ambiguous process outcome.
		return closeErr
	}
}

func (process *unixLaunchedProcess) killAndWaitOwned() error {
	deadline := time.Now().Add(descendantCleanupTimeout)
	var firstFailure error
	for {
		if err := errors.Join(process.refreshDescendants(), process.signalOwned(unix.SIGKILL)); err != nil && firstFailure == nil {
			firstFailure = err
		}
		process.state.Lock()
		remaining := len(process.children)
		process.state.Unlock()
		if remaining == 0 {
			return firstFailure
		}
		if !time.Now().Before(deadline) {
			return errors.Join(firstFailure, errors.New("managed daemon Unix descendant cleanup timed out"))
		}
		time.Sleep(time.Millisecond)
	}
}

func (process *unixLaunchedProcess) trackDescendants() {
	defer close(process.trackerDone)
	ticker := time.NewTicker(descendantDiscoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			process.recordDiscovery(process.refreshDescendants())
		case <-process.stopTrack:
			return
		}
	}
}

func (process *unixLaunchedProcess) serveRegistrations() {
	defer close(process.serverDone)
	request := make([]byte, descendantRegistrationBytes)
	response := []byte{0}
	for {
		if _, err := io.ReadFull(process.registry, request); err != nil {
			if !errors.Is(err, os.ErrClosed) && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				process.recordHistory(fmt.Errorf("read managed daemon descendant registration: %w", err))
			}
			return
		}
		pidValue := binary.BigEndian.Uint64(request)
		if pidValue == 0 || pidValue > uint64(^uint(0)>>1) {
			process.recordHistory(errors.New("managed daemon descendant registration is invalid"))
			response[0] = 0
		} else if int(pidValue) == process.rootPID {
			process.state.Lock()
			process.rootOpened = true
			process.state.Unlock()
			response[0] = 1
		} else if err := process.registerDescendant(int(pidValue)); err != nil {
			process.recordHistory(err)
			response[0] = 0
		} else {
			response[0] = 1
		}
		if _, err := process.registry.Write(response); err != nil {
			if !errors.Is(err, os.ErrClosed) {
				process.recordHistory(fmt.Errorf("acknowledge managed daemon descendant registration: %w", err))
			}
			return
		}
	}
}

func (process *unixLaunchedProcess) registerDescendant(pid int) error {
	if pid == process.rootPID {
		return errors.New("managed daemon root cannot register as its own descendant")
	}
	process.discovery.Lock()
	defer process.discovery.Unlock()
	records, err := processSnapshot()
	if err != nil {
		return fmt.Errorf("inspect managed daemon descendant registration: %w", err)
	}
	byPID := indexProcesses(records)
	process.state.Lock()
	defer process.state.Unlock()
	process.discoverLocked(byPID)
	record, exists := byPID[pid]
	if !exists || !process.isOwnedLocked(record, byPID) {
		return errors.New("managed daemon descendant registration was rejected")
	}
	process.children[pid] = record.identity
	return nil
}

func (process *unixLaunchedProcess) refreshDescendants() error {
	process.discovery.Lock()
	defer process.discovery.Unlock()
	records, err := processSnapshot()
	if err != nil {
		return fmt.Errorf("inspect managed daemon descendants: %w", err)
	}
	byPID := indexProcesses(records)
	process.state.Lock()
	defer process.state.Unlock()
	process.discoverLocked(byPID)
	return nil
}

func (process *unixLaunchedProcess) discoverLocked(byPID map[int]processRecord) {
	root, rootExists := byPID[process.rootPID]
	if process.root.isZero() && rootExists && !process.rootDone {
		process.root = root.identity
	}
	owned := make(map[int]struct{}, len(process.children)+1)
	if rootExists && root.identity == process.root {
		owned[process.rootPID] = struct{}{}
	}
	for pid, identity := range process.children {
		if record, exists := byPID[pid]; exists && record.identity == identity {
			owned[pid] = struct{}{}
		} else {
			delete(process.children, pid)
		}
	}
	for changed := true; changed; {
		changed = false
		for _, record := range byPID {
			if _, known := owned[record.pid]; known {
				continue
			}
			if _, parentOwned := owned[record.ppid]; !parentOwned {
				continue
			}
			process.children[record.pid] = record.identity
			owned[record.pid] = struct{}{}
			changed = true
		}
	}
}

func (process *unixLaunchedProcess) isOwnedLocked(record processRecord, byPID map[int]processRecord) bool {
	visited := make(map[int]struct{})
	for current := record; current.pid > 0; {
		if current.pid == process.rootPID {
			return current.identity == process.root
		}
		if identity, exists := process.children[current.pid]; exists && identity == current.identity {
			return true
		}
		if _, duplicate := visited[current.pid]; duplicate {
			return false
		}
		visited[current.pid] = struct{}{}
		parent, exists := byPID[current.ppid]
		if !exists {
			return false
		}
		current = parent
	}
	return false
}

func (process *unixLaunchedProcess) signalOwned(signal unix.Signal) error {
	process.discovery.Lock()
	defer process.discovery.Unlock()
	records, err := processSnapshot()
	if err != nil {
		return fmt.Errorf("verify managed daemon descendants before signaling: %w", err)
	}
	byPID := indexProcesses(records)
	process.state.Lock()
	process.discoverLocked(byPID)
	rootIdentity := process.root
	children := make(map[int]processIdentity, len(process.children))
	maps.Copy(children, process.children)
	process.state.Unlock()

	return errors.Join(
		signalOriginalGroup(signal, process.rootPID, rootIdentity, children, byPID),
		signalEscapedDescendants(signal, process.rootPID, children, byPID),
	)
}

func signalOriginalGroup(
	signal unix.Signal,
	rootPID int,
	rootIdentity processIdentity,
	children map[int]processIdentity,
	processes map[int]processRecord,
) error {
	root, rootExists := processes[rootPID]
	rootOwned := rootExists && root.identity == rootIdentity && root.pgid == rootPID
	if !rootOwned && !ownsOriginalGroup(children, processes, rootPID) {
		return nil
	}
	if err := unix.Kill(-rootPID, signal); err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("signal managed daemon process group: %w", err)
	}
	return nil
}

func signalEscapedDescendants(
	signal unix.Signal,
	rootPID int,
	children map[int]processIdentity,
	processes map[int]processRecord,
) error {
	var failures []error
	for pid, identity := range children {
		record, exists := processes[pid]
		if !exists || record.identity != identity || record.pgid == rootPID {
			continue
		}
		if err := unix.Kill(pid, signal); err != nil && !errors.Is(err, unix.ESRCH) {
			failures = append(failures, fmt.Errorf("signal managed daemon escaped descendant: %w", err))
		}
	}
	return errors.Join(failures...)
}

func ownsOriginalGroup(children map[int]processIdentity, processes map[int]processRecord, pgid int) bool {
	for pid, identity := range children {
		record, exists := processes[pid]
		if exists && record.identity == identity && record.pgid == pgid {
			return true
		}
	}
	return false
}

func (process *unixLaunchedProcess) recordDiscovery(err error) {
	if err == nil {
		return
	}
	process.state.Lock()
	defer process.state.Unlock()
	// Process-table discovery failures can repeat on every sampling interval.
	// One failure is enough to make the containment uncertainty observable
	// without allowing an unbounded error journal.
	for _, recorded := range process.history {
		if errors.Is(recorded, err) || recorded.Error() == err.Error() {
			return
		}
	}
	process.history = append(process.history, err)
}

func (process *unixLaunchedProcess) recordHistory(err error) {
	if err == nil {
		return
	}
	process.state.Lock()
	process.history = append(process.history, err)
	process.state.Unlock()
}

func indexProcesses(records []processRecord) map[int]processRecord {
	result := make(map[int]processRecord, len(records))
	for _, record := range records {
		result[record.pid] = record
	}
	return result
}

func withDescendantRegistry(environment []string) []string {
	return withFixedEnvironment(environment, descendantRegistryEnvironment, strconv.Itoa(descendantRegistryFD))
}

func withFixedEnvironment(environment []string, name, fixedValue string) []string {
	result := withoutEnvironment(environment, name)
	return append(result, name+"="+fixedValue)
}

func withoutEnvironment(environment []string, name string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		if !strings.HasPrefix(value, prefix) {
			result = append(result, value)
		}
	}
	return result
}

func closeFiles(files ...io.Closer) error {
	var failures []error
	for _, file := range files {
		if file == nil {
			continue
		}
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// DescendantRegistry is the daemon-owned explicit registration contract for
// child processes that may detach from the daemon's process group. It must be
// opened before the daemon starts any child so CloseOnExec prevents accidental
// inheritance. Only the daemon root may use a registry concurrently.
type DescendantRegistry struct {
	mu     sync.Mutex
	file   *os.File
	closed bool
}

const descendantGateEnvironment = "SPICE_AGENT_DESCENDANT_GATE_FD"

// OpenDescendantRegistry adopts the supervisor-provided registry endpoint.
func OpenDescendantRegistry() (*DescendantRegistry, error) {
	value, exists := os.LookupEnv(descendantRegistryEnvironment)
	if !exists || value != strconv.Itoa(descendantRegistryFD) {
		return nil, errors.New("managed daemon descendant registry is unavailable")
	}
	unix.CloseOnExec(descendantRegistryFD)
	registry := &DescendantRegistry{file: os.NewFile(descendantRegistryFD, "managed-daemon-descendant-registration")}
	if registry.file == nil {
		return nil, errors.New("managed daemon descendant registry is unavailable")
	}
	timeout := unix.NsecToTimeval(descendantAdoptionTimeout.Nanoseconds())
	if err := setRegistrySocketTimeout(registry.file, &timeout); err != nil {
		return nil, errors.Join(err, registry.file.Close())
	}
	exchangeErr := registry.exchange(os.Getpid())
	clearErr := setRegistrySocketTimeout(registry.file, &unix.Timeval{})
	if exchangeErr != nil || clearErr != nil {
		return nil, errors.Join(exchangeErr, clearErr, registry.file.Close())
	}
	return registry, nil
}

func setRegistrySocketTimeout(file *os.File, timeout *unix.Timeval) error {
	if file == nil {
		return errors.New("managed daemon descendant registry is unavailable")
	}
	fd := int(file.Fd())
	return errors.Join(
		unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, timeout),
		unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, timeout),
	)
}

// AdoptRootRegistry adopts managed-launch containment when the supervisor
// endpoint is present. Explicit serve has no inherited endpoint and receives an
// inert lifecycle handle; a present but malformed endpoint fails closed.
func AdoptRootRegistry() (RootRegistry, error) {
	if _, managed := os.LookupEnv(descendantRegistryEnvironment); !managed {
		return inactiveRootRegistry{}, nil
	}
	registry, err := OpenDescendantRegistry()
	if err != nil {
		return nil, err
	}
	return registry, nil
}

// Register records a successfully started direct child while it remains
// parented. It is a defense-in-depth primitive for children that cannot detach.
// Use Start for any executable that can fork, setpgid, or reparent; calling
// ordinary exec.Cmd.Start followed by Register has an unavoidable race.
func (registry *DescendantRegistry) Register(process *os.Process) error {
	if registry == nil || process == nil || process.Pid <= 0 {
		return errors.New("managed daemon descendant registration is invalid")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed || registry.file == nil {
		return errors.New("managed daemon descendant registry is closed")
	}
	return registry.exchangeLocked(process.Pid)
}

func (registry *DescendantRegistry) exchange(pid int) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed || registry.file == nil {
		return errors.New("managed daemon descendant registry is closed")
	}
	return registry.exchangeLocked(pid)
}

func (registry *DescendantRegistry) exchangeLocked(pid int) error {
	request := make([]byte, descendantRegistrationBytes)
	binary.BigEndian.PutUint64(request, uint64(pid))
	if _, err := registry.file.Write(request); err != nil {
		return fmt.Errorf("submit managed daemon descendant registration: %w", err)
	}
	response := []byte{0}
	if _, err := io.ReadFull(registry.file, response); err != nil {
		return fmt.Errorf("read managed daemon descendant registration result: %w", err)
	}
	if response[0] != 1 {
		return errors.New("managed daemon descendant registration was rejected")
	}
	return nil
}

// Start launches command through the fail-closed cooperative registration
// gate. The child executable must call AwaitDescendantRegistration before any
// user workload. Start requires a bounded context, waits for that pre-work
// acknowledgement, identity-registers the still-parented child, and only then
// releases it. On failure Start kills and reaps command itself.
//
// This handshake closes the ordinary Start-to-Register race for cooperating
// daemon-owned executables. Unix cannot prove that an arbitrary executable did
// not perform work before acknowledging, so non-cooperating executables are
// unsupported by this containment contract.
func (registry *DescendantRegistry) Start(ctx context.Context, command *exec.Cmd) error {
	if err := validateDescendantStart(registry, ctx, command); err != nil {
		return err
	}
	gate, childGate, err := prepareDescendantGate(command)
	if err != nil {
		return err
	}
	if err = command.Start(); err != nil {
		return errors.Join(err, closeFiles(gate, childGate))
	}
	launchErr := registry.releaseDescendant(ctx, command, gate, childGate.Close())
	if launchErr == nil {
		return nil
	}
	killErr := unix.Kill(-command.Process.Pid, unix.SIGKILL)
	if errors.Is(killErr, unix.ESRCH) {
		killErr = nil
	}
	return errors.Join(launchErr, killErr, command.Wait())
}

func validateDescendantStart(registry *DescendantRegistry, ctx context.Context, command *exec.Cmd) error {
	if registry == nil || command == nil {
		return errors.New("managed daemon descendant launch is invalid")
	}
	if ctx == nil {
		return errors.New("managed daemon descendant launch context is required")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return errors.New("managed daemon descendant launch requires a deadline")
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if command.SysProcAttr != nil && (command.SysProcAttr.Setsid || command.SysProcAttr.Pgid != 0) {
		return errors.New("managed daemon descendant has incompatible process-group configuration")
	}
	return nil
}

func prepareDescendantGate(command *exec.Cmd) (*os.File, *os.File, error) {
	gateFDs, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("create managed daemon descendant gate: %w", err)
	}
	unix.CloseOnExec(gateFDs[0])
	unix.CloseOnExec(gateFDs[1])
	gate := os.NewFile(uintptr(gateFDs[0]), "managed-daemon-descendant-gate")
	childGate := os.NewFile(uintptr(gateFDs[1]), "managed-daemon-descendant-gate-child")
	if gate == nil || childGate == nil {
		return nil, nil, errors.Join(
			errors.New("create managed daemon descendant gate files"), closeFiles(gate, childGate),
		)
	}
	childFD := 3 + len(command.ExtraFiles)
	command.ExtraFiles = append(command.ExtraFiles, childGate)
	environment := command.Env
	if environment == nil {
		environment = os.Environ()
	}
	environment = withoutEnvironment(environment, descendantRegistryEnvironment)
	command.Env = withFixedEnvironment(environment, descendantGateEnvironment, strconv.Itoa(childFD))
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
	return gate, childGate, nil
}

func (registry *DescendantRegistry) releaseDescendant(
	ctx context.Context,
	command *exec.Cmd,
	gate *os.File,
	childCloseErr error,
) error {
	readyErr := childCloseErr
	var interruptErr error
	gateFD := int(gate.Fd())
	shutdownDone := make(chan error, 1)
	cancelGate := context.AfterFunc(ctx, func() {
		err := unix.Shutdown(gateFD, unix.SHUT_RDWR)
		if errors.Is(err, unix.ENOTCONN) || errors.Is(err, unix.EINVAL) {
			err = nil
		}
		shutdownDone <- err
	})
	if readyErr == nil {
		ready := []byte{0}
		_, readyErr = io.ReadFull(gate, ready)
		if readyErr == nil && ready[0] != 1 {
			readyErr = errors.New("managed daemon descendant sent an invalid gate acknowledgement")
		}
	}
	if !cancelGate() {
		interruptErr = <-shutdownDone
	}
	if readyErr == nil {
		readyErr = context.Cause(ctx)
	}
	if readyErr == nil {
		readyErr = registry.Register(command.Process)
	}
	if readyErr == nil {
		_, readyErr = gate.Write([]byte{1})
	}
	gateCloseErr := gate.Close()
	return errors.Join(readyErr, interruptErr, gateCloseErr)
}

// AwaitDescendantRegistration is the first entrypoint operation a daemon-owned
// child performs before user workload; all package init functions in that
// executable must therefore be side-effect-free. It acknowledges the pre-work
// boundary, waits until the supervisor
// identity-registers the process, and then closes the inherited gate. An
// arbitrary executable or grandchild chain is unsupported unless every launch
// cooperates with this gate.
func AwaitDescendantRegistration() error {
	value, exists := os.LookupEnv(descendantGateEnvironment)
	if !exists {
		return errors.New("managed daemon descendant gate is unavailable")
	}
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 {
		return errors.New("managed daemon descendant gate is invalid")
	}
	unix.CloseOnExec(fd)
	gate := os.NewFile(uintptr(fd), "managed-daemon-descendant-gate")
	if gate == nil {
		return errors.New("managed daemon descendant gate is invalid")
	}
	defer gate.Close() //nolint:errcheck // The protocol result is authoritative.
	if _, err = gate.Write([]byte{1}); err != nil {
		return fmt.Errorf("acknowledge managed daemon descendant gate: %w", err)
	}
	release := []byte{0}
	if _, err = io.ReadFull(gate, release); err != nil {
		return fmt.Errorf("await managed daemon descendant release: %w", err)
	}
	if release[0] != 1 {
		return errors.New("managed daemon descendant release is invalid")
	}
	return nil
}

// Close releases the daemon side of the registration channel.
func (registry *DescendantRegistry) Close() error {
	if registry == nil {
		return nil
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed {
		return nil
	}
	registry.closed = true
	return registry.file.Close()
}
