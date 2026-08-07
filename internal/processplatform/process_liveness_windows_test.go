//go:build windows

package processplatform

import (
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func assertPlatformProcessStopped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err != nil {
			return
		}
		var code uint32
		queryErr := windows.GetExitCodeProcess(handle, &code)
		closeErr := windows.CloseHandle(handle)
		if queryErr != nil || closeErr != nil || code != 259 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("process %d remained active", pid)
}
