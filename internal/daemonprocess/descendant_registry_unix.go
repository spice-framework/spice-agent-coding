//go:build linux || darwin

package daemonprocess

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// DescendantRegistry is the daemon-owned explicit registration contract for
// child processes that may detach from the daemon's process group. It must be
// opened before the daemon starts any child so CloseOnExec prevents accidental
// inheritance. Only the daemon root may use a registry concurrently.
type DescendantRegistry struct {
	mu     sync.Mutex
	file   *os.File
	closed bool
}

// NewDescendantRegistry adopts the supervisor-provided registry endpoint.
func NewDescendantRegistry() (*DescendantRegistry, error) {
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
	if err := registry.setSocketTimeout(&timeout); err != nil {
		return nil, errors.Join(err, registry.file.Close())
	}
	exchangeErr := registry.exchange(os.Getpid())
	clearErr := registry.setSocketTimeout(&unix.Timeval{})
	if exchangeErr != nil || clearErr != nil {
		return nil, errors.Join(exchangeErr, clearErr, registry.file.Close())
	}
	return registry, nil
}

func (registry *DescendantRegistry) setSocketTimeout(timeout *unix.Timeval) error {
	if registry == nil || registry.file == nil {
		return errors.New("managed daemon descendant registry is unavailable")
	}
	fd := int(registry.file.Fd())
	return errors.Join(
		unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, timeout),
		unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, timeout),
	)
}

func (registry *DescendantRegistry) adoptRoot() (RootRegistry, error) {
	if _, managed := os.LookupEnv(descendantRegistryEnvironment); !managed {
		return inactiveRootRegistry{}, nil
	}
	adopted, err := NewDescendantRegistry()
	if err != nil {
		return nil, err
	}
	return adopted, nil
}

// Register records a successfully started direct child while it remains
// parented. It is a defense-in-depth primitive for children that cannot detach.
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
	if pid <= 0 {
		return errors.New("managed daemon descendant registration PID is invalid")
	}
	request := make([]byte, descendantRegistrationBytes)
	// #nosec G115 -- the preceding positive check makes the OS PID exactly
	// representable by uint64 on every supported Go architecture.
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
// gate and returns only after the still-parented child is identity-registered.
func (registry *DescendantRegistry) Start(ctx context.Context, command *exec.Cmd) error {
	if err := registry.validateStart(ctx, command); err != nil {
		return err
	}
	gate, childGate, err := registry.prepareGate(command)
	if err != nil {
		return err
	}
	if err = command.Start(); err != nil {
		return errors.Join(err, (unixFileSet{}).close(gate, childGate))
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

func (registry *DescendantRegistry) validateStart(ctx context.Context, command *exec.Cmd) error {
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

func (registry *DescendantRegistry) prepareGate(command *exec.Cmd) (*os.File, *os.File, error) {
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
			errors.New("create managed daemon descendant gate files"), (unixFileSet{}).close(gate, childGate),
		)
	}
	childFD := 3 + len(command.ExtraFiles)
	command.ExtraFiles = append(command.ExtraFiles, childGate)
	environment := command.Env
	if environment == nil {
		environment = os.Environ()
	}
	environment = (unixEnvironment{}).without(environment, descendantRegistryEnvironment)
	command.Env = (unixEnvironment{}).withFixed(environment, descendantGateEnvironment, strconv.Itoa(childFD))
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
