//go:build unix

package installedacceptance

import (
	"errors"

	"golang.org/x/sys/unix"
)

// managedProcessWitness retains the exact PID published with the daemon's
// random instance identity. Unix signal-zero is the portable non-mutating
// liveness probe; the assertion runs immediately after the owning terminal
// joins managed containment, minimizing the residual PID-reuse window.
type managedProcessWitness struct {
	pid int
}

func openManagedProcessWitness(pid uint32) (*managedProcessWitness, error) {
	if pid == 0 {
		return nil, errors.New("managed process ID is invalid")
	}
	return &managedProcessWitness{pid: int(pid)}, nil
}

func (witness *managedProcessWitness) Exited() (bool, error) {
	if witness == nil || witness.pid <= 0 {
		return false, errors.New("managed process witness is unavailable")
	}
	err := unix.Kill(witness.pid, 0)
	switch {
	case errors.Is(err, unix.ESRCH):
		return true, nil
	case err == nil, errors.Is(err, unix.EPERM):
		return false, nil
	default:
		return false, err
	}
}

func (*managedProcessWitness) Close() error { return nil }
