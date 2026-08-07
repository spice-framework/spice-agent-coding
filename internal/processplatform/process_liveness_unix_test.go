//go:build linux || darwin

package processplatform

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

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
