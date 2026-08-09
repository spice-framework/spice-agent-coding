//go:build linux || darwin

package processplatform

import (
	"errors"
	"testing"
	"time"

	agentprocess "github.com/spice-framework/spice-agent/process"
	"golang.org/x/sys/unix"
)

func waitForChildOwnership(t *testing.T, process agentprocess.Process, pid int) {
	t.Helper()
	owned, ok := process.(*unixProcess)
	if !ok {
		t.Fatalf("owned process = %T, want *unixProcess", process)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		owned.stateMu.Lock()
		identity, tracked := owned.children[pid]
		owned.stateMu.Unlock()
		if tracked && !identity.IsZero() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("child process %d was not tracked", pid)
}

func assertPlatformProcessStopped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("process %d remained active", pid)
}
