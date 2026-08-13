//go:build spice_release_artifacts && windows

package installedacceptance

import (
	"errors"

	"golang.org/x/sys/windows"
)

func (witness *managedProcessWitness) terminateForFailureCleanup() error {
	if witness == nil || witness.handle == 0 {
		return errors.New("managed process witness is unavailable")
	}
	exited, err := witness.Exited()
	if err != nil || exited {
		return err
	}
	return windows.TerminateProcess(witness.handle, 1)
}
