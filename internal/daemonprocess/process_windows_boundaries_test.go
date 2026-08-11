//go:build windows

package daemonprocess

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent-coding/internal/testpath"

	"golang.org/x/sys/windows"
)

func TestWindowsPlatformBoundaryValidation(t *testing.T) {
	t.Parallel()

	registry, err := (RootRegistryFactory{}).Adopt()
	if err != nil {
		t.Fatal(err)
	}
	if err = registry.Close(); err != nil {
		t.Fatal(err)
	}

	directory := testpath.NewSupport().TempDir(t)
	valid := processSpec{
		executable:  filepath.Join(directory, (&Starter{}).daemonExecutableName()),
		argument:    daemonArgument,
		directory:   directory,
		environment: []string{"PATH=value"},
		stderr:      io.Discard,
		waitDelay:   time.Second,
	}
	tests := []struct {
		name   string
		mutate func(*processSpec)
	}{
		{name: "empty executable", mutate: func(spec *processSpec) { spec.executable = "" }},
		{name: "relative executable", mutate: func(spec *processSpec) { spec.executable = "spice-agentd.exe" }},
		{name: "noncanonical executable", mutate: func(spec *processSpec) { spec.executable += string(filepath.Separator) + ".." }},
		{name: "unexpected argument", mutate: func(spec *processSpec) { spec.argument = "other" }},
		{name: "nul executable", mutate: func(spec *processSpec) { spec.executable += "\x00" }},
		{name: "empty directory", mutate: func(spec *processSpec) { spec.directory = "" }},
		{name: "relative directory", mutate: func(spec *processSpec) { spec.directory = "relative" }},
		{name: "noncanonical directory", mutate: func(spec *processSpec) { spec.directory += string(filepath.Separator) + "." }},
		{name: "nul directory", mutate: func(spec *processSpec) { spec.directory += "\x00" }},
		{name: "missing stderr", mutate: func(spec *processSpec) { spec.stderr = nil }},
		{name: "empty wait delay", mutate: func(spec *processSpec) { spec.waitDelay = 0 }},
		{name: "nul environment", mutate: func(spec *processSpec) { spec.environment = []string{"A=b\x00c"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			spec := valid
			test.mutate(&spec)
			if err := (processFactory{}).validate(spec); err == nil {
				t.Fatal("invalid Windows process specification succeeded")
			}
			if child, err := (processFactory{}).start(spec); err == nil || child != nil {
				t.Fatalf("startProcess(invalid) = %v, %v", child, err)
			}
		})
	}
}

func TestWindowsParameterAndEnvironmentBoundaries(t *testing.T) {
	t.Parallel()

	empty, err := (windowsEnvironment{}).block(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 2 || empty[0] != 0 || empty[1] != 0 {
		t.Fatalf("empty environment block = %#v", empty)
	}

	for _, value := range []string{"", "=broken", "missing"} {
		if key, ok := (windowsEnvironment{}).key(value); ok || key != "" {
			t.Fatalf("windowsEnvironmentKey(%q) = %q, %t", value, key, ok)
		}
	}
	if key, ok := (windowsEnvironment{}).key("EMPTY="); !ok || key != "EMPTY" {
		t.Fatalf("empty-value environment key = %q, %t", key, ok)
	}

	directory := testpath.NewSupport().TempDir(t)
	valid := processSpec{
		executable: filepath.Join(directory, (&Starter{}).daemonExecutableName()), argument: daemonArgument,
		directory: directory, environment: []string{"A=b"},
	}
	parameters := []struct {
		name   string
		mutate func(*processSpec)
	}{
		{name: "executable encoding", mutate: func(spec *processSpec) { spec.executable += "\x00" }},
		{name: "command encoding", mutate: func(spec *processSpec) { spec.argument += "\x00" }},
		{name: "directory encoding", mutate: func(spec *processSpec) { spec.directory += "\x00" }},
		{name: "environment encoding", mutate: func(spec *processSpec) { spec.environment = []string{"invalid"} }},
	}
	for _, test := range parameters {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			spec := valid
			test.mutate(&spec)
			application, command, working, environment, err := (processFactory{}).parameters(spec)
			if err == nil || application != nil || command != nil || working != nil || environment != nil {
				t.Fatalf("windowsProcessParameters(invalid) = %v, %v, %v, %v, %v",
					application, command, working, environment, err)
			}
		})
	}
}

func TestWindowsWaitAndJobKernelBoundaries(t *testing.T) {
	t.Parallel()

	for _, handle := range []windows.Handle{0, windows.InvalidHandle} {
		if err := (windowsHandleOwner{}).wait(handle, time.Second); err == nil {
			t.Fatalf("waitWindowsHandle(%v) succeeded", handle)
		}
		if err := (windowsJob{}).waitEmpty(handle, time.Second); err == nil {
			t.Fatalf("waitWindowsJobEmpty(%v) succeeded", handle)
		}
	}

	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := (windowsHandleOwner{}).close(event); closeErr != nil {
			t.Errorf("close event: %v", closeErr)
		}
	})
	if err = (windowsHandleOwner{}).wait(event, time.Nanosecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unsignaled event wait = %v", err)
	}
	if err = windows.SetEvent(event); err != nil {
		t.Fatal(err)
	}
	if err = (windowsHandleOwner{}).wait(event, time.Second); err != nil {
		t.Fatalf("signaled event wait = %v", err)
	}

	job, err := (windowsJob{}).open()
	if err != nil {
		t.Fatal(err)
	}
	if err = (windowsJob{}).waitEmpty(job, time.Second); err != nil {
		closeErr := (windowsHandleOwner{}).close(job)
		t.Fatalf("empty job wait = %v; close = %v", err, closeErr)
	}
	if err = (windowsHandleOwner{}).close(job); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsFailedStartRetainsOwnedCleanup(t *testing.T) {
	t.Parallel()

	job, err := (windowsJob{}).open()
	if err != nil {
		t.Fatal(err)
	}
	pipes, err := (&windowsProcessPipes{}).open()
	if err != nil {
		closeErr := (windowsHandleOwner{}).close(job)
		t.Fatalf("create process pipes: %v; close job: %v", err, closeErr)
	}
	wrongTypeProcess, err := windows.CreateEvent(nil, 1, 1, nil)
	if err != nil {
		closeErr := pipes.closeAll()
		jobErr := (windowsHandleOwner{}).close(job)
		t.Fatalf("create wrong-type process handle: %v; pipe close = %v; job close = %v", err, closeErr, jobErr)
	}
	spec := processSpec{stderr: io.Discard, waitDelay: time.Second}
	child, startErr := (processFactory{}).finish(job, pipes, windows.ProcessInformation{
		Process: wrongTypeProcess,
	}, spec)
	if startErr == nil || child == nil {
		t.Fatalf("failed start = %T, %v", child, startErr)
	}
	pipes.releaseAll()
	failed, ok := child.(*windowsProcess)
	if !ok {
		t.Fatalf("failed child = %T", child)
	}
	t.Cleanup(func() { _ = failed.Close() }) //nolint:errcheck // The assertions below inspect the authoritative cleanup result.
	if waitErr := failed.Wait(); waitErr != nil {
		t.Fatalf("signaled wrong-type process wait = %v", waitErr)
	}
	firstCloseErr := failed.Close()
	if firstCloseErr == nil {
		t.Fatal("failed process cleanup lost its containment failure history")
	}
	if secondCloseErr := failed.Close(); !errors.Is(secondCloseErr, firstCloseErr) {
		t.Fatalf("idempotent failed process cleanup = %v, want cached %v", secondCloseErr, firstCloseErr)
	}
	failed.mu.Lock()
	defer failed.mu.Unlock()
	if !failed.closed || failed.process != 0 || failed.job != 0 || failed.thread != 0 {
		t.Fatalf("failed process retained transferred handles: %#v", failed)
	}
}

func TestWindowsProcessFailureHistoryAndExitStatus(t *testing.T) {
	t.Parallel()

	failure := &windowsExitError{code: 37}
	if failure.ExitCode() != 37 || !strings.Contains(failure.Error(), "37") {
		t.Fatalf("exit failure = %q, %d", failure.Error(), failure.ExitCode())
	}
	var nilFailure *windowsExitError
	if nilFailure.ExitCode() != 0 {
		t.Fatal("nil exit error has a status")
	}
	wrongTypeProcess, err := windows.CreateEvent(nil, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	locallyOwned := true
	t.Cleanup(func() {
		if locallyOwned {
			if closeErr := (windowsHandleOwner{}).close(wrongTypeProcess); closeErr != nil {
				t.Errorf("close locally owned wrong-type handle: %v", closeErr)
			}
		}
	})
	if outcomeErr := (processFactory{}).outcome(wrongTypeProcess); outcomeErr != nil {
		t.Fatalf("signaled wrong-type process outcome = %v", outcomeErr)
	}

	process := &windowsProcess{}
	if killErr := process.Kill(); killErr != nil {
		t.Fatalf("kill empty process: %v", killErr)
	}
	process.recordFailure(nil)
	sentinel := errors.New("recorded containment failure")
	process.recordFailure(sentinel)
	if len(process.failures) != 1 || !errors.Is(process.failures[0], sentinel) {
		t.Fatalf("failure history = %#v", process.failures)
	}

	invalid := &windowsProcess{process: wrongTypeProcess}
	locallyOwned = false
	t.Cleanup(func() { _ = invalid.Close() }) //nolint:errcheck // The assertions below inspect the authoritative cleanup result.
	if terminationErr := invalid.Terminate(); terminationErr == nil {
		t.Fatal("termination with wrong-type process handle succeeded")
	}
	if len(invalid.failures) != 1 {
		t.Fatalf("invalid termination failures = %#v", invalid.failures)
	}
	firstCloseErr := invalid.Close()
	if firstCloseErr == nil {
		t.Fatal("invalid process cleanup succeeded")
	}
	if secondCloseErr := invalid.Close(); !errors.Is(secondCloseErr, firstCloseErr) {
		t.Fatalf("idempotent invalid process cleanup = %v, want cached %v", secondCloseErr, firstCloseErr)
	}
	invalid.mu.Lock()
	if !invalid.closed || invalid.process != 0 {
		invalid.mu.Unlock()
		t.Fatalf("invalid process retained transferred handle: %#v", invalid)
	}
	invalid.mu.Unlock()

	closed := &windowsProcess{closed: true}
	if terminationErr := closed.Terminate(); terminationErr != nil {
		t.Fatalf("closed process termination = %v", terminationErr)
	}

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = write.Close(); err != nil {
		closeErr := read.Close()
		t.Fatalf("close stderr writer: %v; close reader: %v", err, closeErr)
	}
	done := make(chan error, 1)
	done <- nil
	close(done)
	completed := &windowsProcess{
		stderr: read, stderrDone: done, waitDelay: time.Second,
		waitCompleted: true, waitErr: sentinel,
	}
	if err = completed.Wait(); !errors.Is(err, sentinel) {
		t.Fatalf("precompleted wait = %v", err)
	}
	if err = completed.Close(); err != nil {
		t.Fatalf("precompleted close = %v", err)
	}
}

func TestWindowsPipeReleaseAndCloseHelpers(t *testing.T) {
	t.Parallel()

	var nilPipes *windowsProcessPipes
	if err := nilPipes.closeChildEnds(); err != nil {
		t.Fatal(err)
	}
	if err := nilPipes.closeAll(); err != nil {
		t.Fatal(err)
	}

	pipes, err := (&windowsProcessPipes{}).open()
	if err != nil {
		t.Fatal(err)
	}
	if err = pipes.closeAll(); err != nil {
		t.Fatal(err)
	}
	pipes.releaseAll()
	if pipes.childInput != 0 || pipes.childOutput != 0 || pipes.childStderr != 0 ||
		pipes.parentInput != nil || pipes.parentStderr != nil {
		t.Fatalf("released pipes retain ownership: %#v", pipes)
	}

	first, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		closeErr := (windowsHandleOwner{}).close(first)
		t.Fatalf("create second event: %v; close first: %v", err, closeErr)
	}
	if err = (windowsHandleOwner{}).closeAll([]windows.Handle{first, second, 0, windows.InvalidHandle}); err != nil {
		t.Fatal(err)
	}
}
