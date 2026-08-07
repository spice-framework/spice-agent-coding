//go:build windows

package runtimepluginfixture_test

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func assertFixtureProcessExited(t *testing.T, pid int) {
	t.Helper()
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return
	}
	if err != nil {
		t.Fatalf("open runtime-plugin fixture process %d: %v", pid, err)
	}
	defer func() {
		if closeErr := windows.CloseHandle(handle); closeErr != nil {
			t.Errorf("close runtime-plugin fixture process handle %d: %v", pid, closeErr)
		}
	}()
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		t.Fatalf("probe runtime-plugin fixture process %d: %v", pid, err)
	}
	if state != windows.WAIT_OBJECT_0 {
		t.Fatalf("runtime-plugin fixture process %d remained alive after Host.Close", pid)
	}
}
