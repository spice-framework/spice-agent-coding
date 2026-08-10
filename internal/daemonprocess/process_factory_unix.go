//go:build linux || darwin

package daemonprocess

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

type processFactory struct{}

func (processFactory) start(spec processSpec) (launchedProcess, error) {
	registryFDs, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("create managed daemon descendant registry: %w", err)
	}
	unix.CloseOnExec(registryFDs[0])
	unix.CloseOnExec(registryFDs[1])
	registry := os.NewFile(uintptr(registryFDs[0]), "managed-daemon-descendant-registry")
	childRegistry := os.NewFile(uintptr(registryFDs[1]), "managed-daemon-descendant-registration")
	if registry == nil || childRegistry == nil {
		closeRegistryErr := (unixFileSet{}).close(registry, childRegistry)
		return nil, errors.Join(errors.New("create managed daemon descendant registry files"), closeRegistryErr)
	}

	// The process outlives its launch context and is canceled through the
	// platform containment boundary, not by killing only its root PID.
	// #nosec G204 -- executable is the validated distribution sibling and the
	// sole argument is the fixed daemon serve argument; no shell is used.
	command := exec.Command(spec.executable, spec.argument) //nolint:noctx // The owned process outlives its launch context.
	command.Dir = spec.directory
	command.Env = (unixEnvironment{}).withRegistry(spec.environment)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.ExtraFiles = []*os.File{childRegistry}
	discard, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, errors.Join(err, (unixFileSet{}).close(registry, childRegistry))
	}
	command.Stdout = discard
	stderr, childStderr, err := os.Pipe()
	if err != nil {
		return nil, errors.Join(err, (unixFileSet{}).close(registry, childRegistry, discard))
	}
	command.Stderr = childStderr
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, errors.Join(err, (unixFileSet{}).close(registry, childRegistry, discard, stderr, childStderr))
	}
	if err = command.Start(); err != nil {
		return nil, errors.Join(err, (unixFileSet{}).close(stdin, registry, childRegistry, discard, stderr, childStderr))
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
