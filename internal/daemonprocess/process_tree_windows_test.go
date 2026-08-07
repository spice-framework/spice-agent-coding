//go:build windows

package daemonprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const immediateTreeMode = "windows-immediate-tree"

// This fixture runs before the Go test harness. If CreateProcess resumes the
// root before job assignment, its first user instruction can escape a child.
func init() { //nolint:gochecknoinits // Process-launch fixture must run before TestMain and TestProcessHelper.
	if os.Getenv(helperModeEnvironment) != immediateTreeMode {
		return
	}
	command := helperChildCommand()
	if err := command.Start(); err != nil {
		os.Exit(95)
	}
	_, _ = fmt.Fprintf(os.Stderr, "child=%d\n", command.Process.Pid)
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		os.Exit(96)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestWindowsJobShutdownTerminatesProcessTree(t *testing.T) {
	starter := helperStarter(t, "tree", t.TempDir(), 1024)
	candidate, err := starter.Start(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process := requireProcess(t, candidate)
	var child int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if child, _ = childPID(process.ProtectedStderr()); child > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if child == 0 {
		t.Fatalf("helper did not report child: %q", process.ProtectedStderr())
	}
	if err = candidate.BeginShutdown(); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err = candidate.Wait(wait); err != nil {
		t.Fatalf("job containment cleanup failed: %v", err)
	}
	assertProcessStopped(t, child)
}

func TestWindowsSuspendedLaunchContainsImmediateDescendant(t *testing.T) {
	starter := helperStarter(t, immediateTreeMode, t.TempDir(), 1024)
	starter.graceful = 10 * time.Millisecond
	starter.terminate = 100 * time.Millisecond

	for iteration := range 12 {
		candidate, err := starter.Start(t.Context())
		if err != nil {
			t.Fatalf("iteration %d start: %v", iteration, err)
		}
		process := requireProcess(t, candidate)
		child := waitForChildPID(t, process)
		if err = candidate.BeginShutdown(); err != nil {
			t.Fatalf("iteration %d shutdown: %v", iteration, err)
		}
		wait, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		err = candidate.Wait(wait)
		cancel()
		if err != nil {
			t.Fatalf("iteration %d containment cleanup: %v", iteration, err)
		}
		assertProcessStopped(t, child)
	}
}

func TestWindowsEnvironmentBlockUsesLastValueAndRequiredOrdering(t *testing.T) {
	block, err := windowsEnvironmentBlock([]string{
		"beta=two",
		"ALPHA=old",
		"=C:=C:\\work",
		"alpha=new",
		"UNICODE=jalapeño",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(block) < 2 || block[len(block)-1] != 0 || block[len(block)-2] != 0 {
		t.Fatalf("environment block lacks double terminator: %v", block)
	}
	decoded := strings.Split(string(utf16.Decode(block[:len(block)-2])), "\x00")
	want := []string{"=C:=C:\\work", "alpha=new", "beta=two", "UNICODE=jalapeño"}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("environment = %#v, want %#v", decoded, want)
	}
	if _, err = windowsEnvironmentBlock([]string{"missing-separator"}); err == nil {
		t.Fatal("invalid environment entry succeeded")
	}
}

func TestWindowsControlFailureReachesContainmentWait(t *testing.T) {
	starter := helperStarter(t, "blocked", t.TempDir(), 1024)
	starter.graceful = 10 * time.Millisecond
	starter.terminate = 10 * time.Millisecond
	candidate, err := starter.Start(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = candidate.BeginShutdown() //nolint:errcheck // Best-effort cleanup after the authoritative assertions.
		wait, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = candidate.Wait(wait) //nolint:errcheck // Best-effort cleanup after the authoritative assertions.
	})
	process := requireProcess(t, candidate)
	windowsChild, ok := process.child.(*windowsProcess)
	if !ok {
		t.Fatalf("child = %T, want *windowsProcess", process.child)
	}
	waitHelperReady(t, process)

	wrongTypeJob, err := windows.CreateEvent(nil, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	locallyOwned := true
	t.Cleanup(func() {
		if locallyOwned {
			if closeErr := closeWindowsHandle(wrongTypeJob); closeErr != nil {
				t.Errorf("close locally owned wrong-type job handle: %v", closeErr)
			}
		}
	})
	windowsChild.mu.Lock()
	realJob := windowsChild.job
	windowsChild.job = wrongTypeJob
	windowsChild.mu.Unlock()
	locallyOwned = false
	if err = closeWindowsHandle(realJob); err != nil {
		t.Fatal(err)
	}
	if err = windowsChild.Terminate(); err == nil {
		t.Fatal("termination through a wrong-type job handle succeeded")
	}
	if err = candidate.BeginShutdown(); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err = candidate.Wait(wait)
	containment, ok := errors.AsType[*ContainmentError](err)
	if !ok || containment == nil {
		t.Fatalf("containment failure was not preserved: %v", err)
	}
	windowsChild.mu.Lock()
	defer windowsChild.mu.Unlock()
	if !windowsChild.closed || windowsChild.job != 0 || windowsChild.process != 0 || windowsChild.thread != 0 {
		t.Fatalf("contained child retained transferred handles: %#v", windowsChild)
	}
}

func waitForChildPID(t *testing.T, process *Process) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if child, found := childPID(process.ProtectedStderr()); found {
			return child
		}
		select {
		case <-process.Done():
			t.Fatalf("immediate-spawn fixture exited: result=%v, stderr=%q", process.Result(), process.ProtectedStderr())
		default:
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("immediate-spawn fixture did not report its child: stderr=%q", process.ProtectedStderr())
	return 0
}

func processStillActive(pid uint32) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // Read-only liveness probe.
	var exitCode uint32
	if windows.GetExitCodeProcess(handle, &exitCode) != nil {
		return false
	}
	return exitCode == 259 // STILL_ACTIVE
}

func assertProcessStopped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processStillActive(uint32(pid)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d remained active", pid)
}
