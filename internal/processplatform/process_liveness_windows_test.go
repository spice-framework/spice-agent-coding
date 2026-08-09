//go:build windows

package processplatform

import (
	"testing"
	"time"

	agentprocess "github.com/spice-framework/spice-agent/process"
	"golang.org/x/sys/windows"
)

func waitForChildOwnership(t *testing.T, process agentprocess.Process, _ int) {
	t.Helper()
	owned, ok := process.(*windowsProcess)
	if !ok {
		t.Fatalf("owned process = %T, want *windowsProcess", process)
	}
	owned.mu.Lock()
	assigned := owned.assigned
	owned.mu.Unlock()
	if !assigned {
		t.Fatal("root process is not assigned to its containment job")
	}
}

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
