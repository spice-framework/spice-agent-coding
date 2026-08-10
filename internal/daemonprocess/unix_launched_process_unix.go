//go:build linux || darwin

package daemonprocess

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"sync"
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
	records, err := (processSnapshotSource{}).snapshot()
	if err != nil {
		return fmt.Errorf("inspect managed daemon descendant registration: %w", err)
	}
	byPID := process.indexProcesses(records)
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
	records, err := (processSnapshotSource{}).snapshot()
	if err != nil {
		return fmt.Errorf("inspect managed daemon descendants: %w", err)
	}
	byPID := process.indexProcesses(records)
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
	records, err := (processSnapshotSource{}).snapshot()
	if err != nil {
		return fmt.Errorf("verify managed daemon descendants before signaling: %w", err)
	}
	byPID := process.indexProcesses(records)
	process.state.Lock()
	process.discoverLocked(byPID)
	rootIdentity := process.root
	children := make(map[int]processIdentity, len(process.children))
	maps.Copy(children, process.children)
	process.state.Unlock()

	return errors.Join(
		process.signalOriginalGroup(signal, process.rootPID, rootIdentity, children, byPID),
		process.signalEscapedDescendants(signal, process.rootPID, children, byPID),
	)
}

func (process *unixLaunchedProcess) signalOriginalGroup(
	signal unix.Signal,
	rootPID int,
	rootIdentity processIdentity,
	children map[int]processIdentity,
	processes map[int]processRecord,
) error {
	root, rootExists := processes[rootPID]
	rootOwned := rootExists && root.identity == rootIdentity && root.pgid == rootPID
	if !rootOwned && !process.ownsOriginalGroup(children, processes, rootPID) {
		return nil
	}
	if err := unix.Kill(-rootPID, signal); err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("signal managed daemon process group: %w", err)
	}
	return nil
}

func (process *unixLaunchedProcess) signalEscapedDescendants(
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

func (process *unixLaunchedProcess) ownsOriginalGroup(
	children map[int]processIdentity,
	processes map[int]processRecord,
	pgid int,
) bool {
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

func (process *unixLaunchedProcess) indexProcesses(records []processRecord) map[int]processRecord {
	result := make(map[int]processRecord, len(records))
	for _, record := range records {
		result[record.pid] = record
	}
	return result
}
