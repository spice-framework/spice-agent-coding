//go:build windows

package daemonprocess

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

type windowsHandleOwner struct{}

func (windowsHandleOwner) wait(handle windows.Handle, timeout time.Duration) error {
	if handle == 0 || handle == windows.InvalidHandle {
		return errors.New("managed daemon Windows process handle is invalid")
	}
	milliseconds := uint32(windows.INFINITE)
	if timeout > 0 {
		milliseconds = uint32(min(timeout.Milliseconds(), int64(windows.INFINITE-1))) // #nosec G115 -- explicitly capped below uint32 maximum.
	}
	event, err := windows.WaitForSingleObject(handle, milliseconds)
	if err != nil {
		return err
	}
	if event == windows.WAIT_OBJECT_0 {
		return nil
	}
	if event == uint32(windows.WAIT_TIMEOUT) {
		return errors.New("managed daemon Windows process cleanup timed out")
	}
	return fmt.Errorf("unexpected managed daemon Windows wait result: %d", event)
}

func (windowsHandleOwner) close(handle windows.Handle) error {
	if handle == 0 || handle == windows.InvalidHandle {
		return nil
	}
	return windows.CloseHandle(handle)
}

func (windowsHandleOwner) closeAll(handles []windows.Handle) error {
	errorsByHandle := make([]error, 0, len(handles))
	for _, handle := range handles {
		errorsByHandle = append(errorsByHandle, (windowsHandleOwner{}).close(handle))
	}
	return errors.Join(errorsByHandle...)
}

func (windowsHandleOwner) closeFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
