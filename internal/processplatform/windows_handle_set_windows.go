//go:build windows

package processplatform

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type windowsHandleSet struct{}

func (windowsHandleSet) close(handles ...windows.Handle) error {
	failures := make([]error, 0, len(handles))
	for _, handle := range handles {
		failures = append(failures, (windowsHandleSet{}).closeHandle(handle))
	}
	return errors.Join(failures...)
}

func (windowsHandleSet) closeHandle(handle windows.Handle) error {
	if handle == 0 || handle == windows.InvalidHandle {
		return nil
	}
	return windows.CloseHandle(handle)
}

func (windowsHandleSet) closeFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
