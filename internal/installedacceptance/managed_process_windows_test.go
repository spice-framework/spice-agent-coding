//go:build windows

package installedacceptance

import (
	"errors"

	"golang.org/x/sys/windows"
)

// managedProcessWitness holds a handle opened while the exact published
// process is alive, so PID reuse cannot satisfy the post-shutdown assertion.
type managedProcessWitness struct {
	handle windows.Handle
}

func openManagedProcessWitness(pid uint32) (*managedProcessWitness, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return nil, err
	}
	return &managedProcessWitness{handle: handle}, nil
}

func (witness *managedProcessWitness) Exited() (bool, error) {
	if witness == nil || witness.handle == 0 {
		return false, errors.New("managed process witness is unavailable")
	}
	state, err := windows.WaitForSingleObject(witness.handle, 0)
	if err != nil {
		return false, err
	}
	switch state {
	case windows.WAIT_OBJECT_0:
		return true, nil
	case uint32(windows.WAIT_TIMEOUT):
		return false, nil
	default:
		return false, errors.New("managed process wait returned an unexpected state")
	}
}

func (witness *managedProcessWitness) Close() error {
	if witness == nil || witness.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(witness.handle)
	witness.handle = 0
	return err
}
