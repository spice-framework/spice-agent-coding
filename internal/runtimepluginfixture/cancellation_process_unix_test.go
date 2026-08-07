//go:build unix

package runtimepluginfixture_test

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func assertFixtureProcessExited(t *testing.T, pid int) {
	t.Helper()
	err := unix.Kill(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return
	}
	if err != nil {
		t.Fatalf("probe runtime-plugin fixture process %d: %v", pid, err)
	}
	t.Fatalf("runtime-plugin fixture process %d remained alive after Host.Close", pid)
}
