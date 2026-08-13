//go:build spice_release_artifacts && unix

package installedacceptance

import (
	"errors"

	"golang.org/x/sys/unix"
)

func (witness *managedProcessWitness) terminateForFailureCleanup() error {
	if witness == nil || witness.pid <= 0 {
		return errors.New("managed process witness is unavailable")
	}
	err := unix.Kill(witness.pid, unix.SIGKILL)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
