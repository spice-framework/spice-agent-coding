package daemonprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent-coding/internal/testpath"
)

const (
	helperModeEnvironment  = "SPICE_DAEMONPROCESS_HELPER_MODE"
	helperValueEnvironment = "SPICE_DAEMONPROCESS_HELPER_VALUE"
)

func TestProcessHelper(t *testing.T) {
	mode := os.Getenv(helperModeEnvironment)
	if mode == "" {
		return
	}
	if len(os.Args) < 2 || os.Args[1] != daemonArgument {
		os.Exit(91)
	}
	_, _ = fmt.Fprintln(os.Stderr, "ready")
	switch mode {
	case "eof":
		copyStdinOrExit(96)
	case "early":
		os.Exit(17)
	case "blocked":
		copyStdinOrExit(97)
		for {
			time.Sleep(time.Hour)
		}
	case "report":
		working, err := os.Getwd()
		if err != nil {
			os.Exit(98)
		}
		_, _ = fmt.Fprintf(os.Stderr, "%s\ncwd=%s\nvalue=%s\n", strings.Repeat("x", 512), working, os.Getenv(helperValueEnvironment))
		copyStdinOrExit(99)
	case "tree":
		command := helperChildCommand()
		if err := command.Start(); err != nil {
			os.Exit(92)
		}
		_, _ = fmt.Fprintf(os.Stderr, "child=%d\n", command.Process.Pid)
		copyStdinOrExit(100)
		for {
			time.Sleep(time.Hour)
		}
	case "orphan":
		command := helperInheritedChildCommand()
		if err := command.Start(); err != nil {
			os.Exit(94)
		}
		_, _ = fmt.Fprintf(os.Stderr, "child=%d\n", command.Process.Pid)
	case "leaf":
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(93)
	}
	os.Exit(0)
}

func copyStdinOrExit(exitCode int) {
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		os.Exit(exitCode)
	}
}

func TestStarterUsesSiblingDiscreteArgumentAndGracefulEOF(t *testing.T) {
	t.Parallel()
	starter := helperStarter(t, "eof", testpath.TempDir(t), 256)
	candidate := requireOwnedCandidate(t, starter)
	if candidate == nil || candidate.Done() == nil {
		t.Fatal("starter returned an invalid candidate")
	}
	process := requireProcess(t, candidate)
	waitHelperReady(t, process)
	if err := candidate.BeginShutdown(); err != nil {
		t.Fatal(err)
	}
	if err := candidate.BeginShutdown(); err != nil {
		t.Fatalf("idempotent shutdown: %v", err)
	}
	wait, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := candidate.Wait(wait); err != nil || candidate.Result() != nil {
		t.Fatalf("graceful candidate = %v, result=%v, stderr=%q", err, candidate.Result(), process.ProtectedStderr())
	}
}

func TestCandidateReportsEarlyExitAndWaitHonorsContext(t *testing.T) {
	t.Parallel()
	early := helperStarter(t, "early", testpath.TempDir(t), 256)
	candidate := requireOwnedCandidate(t, early)
	wait, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err := candidate.Wait(wait)
	if err != nil || candidate.Result() == nil {
		t.Fatalf("early exit containment/result = %v/%v", err, candidate.Result())
	}

	blocked := helperStarter(t, "blocked", testpath.TempDir(t), 256)
	candidate = requireOwnedCandidate(t, blocked)
	waitHelperReady(t, requireProcess(t, candidate))
	short, stop := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer stop()
	if err = candidate.Wait(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("context-aware wait = %v", err)
	}
	if err = candidate.BeginShutdown(); err != nil {
		t.Fatal(err)
	}
	joined, stopJoin := context.WithTimeout(t.Context(), 3*time.Second)
	defer stopJoin()
	if err = candidate.Wait(joined); err != nil {
		t.Fatalf("escalated containment cleanup: %v", err)
	}
	select {
	case <-candidate.Done():
	default:
		t.Fatal("escalated candidate was not reaped")
	}
}

func TestStarterPreservesWorkingDirectoryEnvironmentAndBoundsStderr(t *testing.T) {
	t.Parallel()
	directory := testpath.TempDir(t)
	starter := helperStarter(t, "report", directory, 1024)
	candidate := requireOwnedCandidate(t, starter)
	process := requireProcess(t, candidate)
	waitHelperReady(t, process)
	if err := candidate.BeginShutdown(); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := candidate.Wait(wait); err != nil {
		t.Fatalf("report candidate = %v, stderr=%q", err, process.ProtectedStderr())
	}
	diagnostics := string(process.ProtectedStderr())
	if len(diagnostics) > 1024 || !strings.Contains(diagnostics, "cwd="+directory) ||
		!strings.Contains(diagnostics, "value=fixture-value") {
		t.Fatalf("protected stderr = %d bytes %q", len(diagnostics), diagnostics)
	}
	copyValue := process.ProtectedStderr()
	if len(copyValue) != 0 {
		copyValue[0] ^= 0xff
		if string(copyValue) == string(process.ProtectedStderr()) {
			t.Fatal("protected stderr exposed mutable storage")
		}
	}
}

func TestStarterRejectsInvalidConfigurationAndRedactsFailures(t *testing.T) {
	t.Parallel()
	directory := testpath.TempDir(t)
	launcher := filepath.Join(directory, launcherExecutableName())
	daemon := filepath.Join(directory, (&Starter{}).daemonExecutableName())
	valid := Config{
		Directory: directory, Environment: []string{"SECRET=credential-material"}, StderrBytes: 64,
		GracefulTimeout: time.Millisecond, TerminateDelay: time.Millisecond,
	}
	if _, err := newTestStarter(Config{}, daemon, launcher); err == nil {
		t.Fatal("unbounded process configuration succeeded")
	}
	if _, err := newTestStarter(valid, filepath.Join(testpath.TempDir(t), (&Starter{}).daemonExecutableName()), launcher); err == nil {
		t.Fatal("non-sibling daemon executable succeeded")
	}
	starter, err := newTestStarter(valid, daemon, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = starter.Start(t.Context()); err == nil || strings.Contains(err.Error(), directory) ||
		strings.Contains(err.Error(), "credential-material") {
		t.Fatalf("start failure leaked process configuration: %v", err)
	}
	for _, rendered := range []string{fmt.Sprint(starter), fmt.Sprintf("%#v", starter), starter.LogValue().String()} {
		if strings.Contains(rendered, directory) || strings.Contains(rendered, "credential-material") {
			t.Fatalf("starter formatting leaked process configuration: %q", rendered)
		}
	}
	if _, err = (*Starter)(nil).Start(t.Context()); err == nil {
		t.Fatal("nil starter succeeded")
	}
	if _, err = starter.Start(nil); err == nil { //nolint:staticcheck // Boundary verifies nil rejection.
		t.Fatal("nil start context succeeded")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = starter.Start(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled start = %v", err)
	}
	var process *Process
	if process.Done() != nil || process.ProtectedStderr() != nil || process.Result() == nil ||
		process.BeginShutdown() == nil || process.Wait(t.Context()) == nil {
		t.Fatal("nil candidate accessors are unsafe")
	}
}

func TestReapJoinsWaitAndContainmentFailures(t *testing.T) {
	waitFailure := errors.New("wait failure")
	containmentCause := errors.New("containment failure")
	process := &Process{
		child: &stubLaunchedProcess{waitErr: waitFailure, closeErr: containmentCause},
		done:  make(chan struct{}),
	}
	process.reap()
	result := process.Result()
	if !errors.Is(result, waitFailure) || errors.Is(result, containmentCause) {
		t.Fatalf("child result = %v", result)
	}
	cleanup := process.Wait(t.Context())
	var containment *ContainmentError
	if !errors.As(cleanup, &containment) || !errors.Is(cleanup, containmentCause) {
		t.Fatalf("cleanup does not preserve containment classification: %v", cleanup)
	}
	if containment.Retryable() {
		t.Fatal("one-shot containment cleanup was classified as retryable")
	}
	if got := cleanup.Error(); strings.Contains(got, containmentCause.Error()) {
		t.Fatalf("cleanup exposed protected containment detail: %q", got)
	}
}

func TestBeginShutdownCachesFirstInputFailure(t *testing.T) {
	inputFailure := errors.New("input close failure")
	process := &Process{
		child:     &stubLaunchedProcess{inputErr: inputFailure},
		done:      make(chan struct{}),
		graceful:  time.Hour,
		terminate: time.Hour,
	}
	first := process.BeginShutdown()
	second := process.BeginShutdown()
	close(process.done)
	if !errors.Is(first, inputFailure) || !errors.Is(second, inputFailure) || first.Error() != second.Error() {
		t.Fatalf("shutdown errors = %v and %v", first, second)
	}
}

func helperStarter(t *testing.T, mode, directory string, stderrBytes int) *Starter {
	t.Helper()
	executable := installHelperExecutable(t)
	launcher := filepath.Join(filepath.Dir(executable), launcherExecutableName())
	environment := append(os.Environ(), helperModeEnvironment+"="+mode, helperValueEnvironment+"=fixture-value")
	starter, err := newTestStarter(Config{
		Directory: directory, Environment: environment, StderrBytes: stderrBytes,
		GracefulTimeout: 2 * time.Second, TerminateDelay: 100 * time.Millisecond,
	}, executable, launcher)
	if err != nil {
		t.Fatal(err)
	}
	return starter
}

func requireOwnedCandidate(t *testing.T, starter *Starter) Candidate {
	t.Helper()
	const maximumAttempts = 8
	classifications := make([]string, 0, maximumAttempts)
	for attempt := range maximumAttempts {
		candidate, err := starter.Start(t.Context())
		if candidate != nil {
			return candidate
		}
		classifications = append(classifications, safeLaunchFailureClass(err))
		if context.Cause(t.Context()) != nil {
			break
		}
		// Hosted race and offline gates can briefly exhaust process or descriptor
		// resources, and Linux overlay-backed workspaces can transiently report
		// ETXTBSY immediately after the test helper is atomically installed. A nil
		// candidate proves ownership was never established, so a bounded test-only
		// retry cannot abandon a process or conceal a containment failure.
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	t.Fatalf("starter returned no owned candidate after %d attempts: %s", maximumAttempts, strings.Join(classifications, ", "))
	panic("unreachable")
}

func safeLaunchFailureClass(err error) string {
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		return fmt.Sprintf("syscall.Errno(%d)", uint64(errno))
	}
	cause := errors.Unwrap(err)
	if cause == nil {
		cause = err
	}
	return fmt.Sprintf("%T", cause)
}

func installHelperExecutable(t *testing.T) string {
	t.Helper()
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := testpath.TempDir(t)
	target := filepath.Join(directory, (&Starter{}).daemonExecutableName())
	staging := target + ".staging"
	source, err := os.Open(current)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close() //nolint:errcheck // Test cleanup owns the source.
	destination, err := os.OpenFile(staging, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(destination, source); err != nil {
		closeErr := destination.Close()
		t.Fatalf("copy helper executable: %v; close destination: %v", err, closeErr)
	}
	if err = destination.Sync(); err != nil {
		closeErr := destination.Close()
		t.Fatalf("sync helper executable: %v; close destination: %v", err, closeErr)
	}
	if err = destination.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(staging, target); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(target, 0o700); err != nil {
		t.Fatal(err)
	}
	return target
}

func launcherExecutableName() string {
	if runtime.GOOS == "windows" {
		return "spice-agent.exe"
	}
	return "spice-agent"
}

func helperChildCommand() *exec.Cmd {
	command := exec.Command(os.Args[0], daemonArgument) // #nosec G204 -- exact current helper executable and fixed argument.
	command.Env = append(os.Environ(), helperModeEnvironment+"=leaf")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command
}

func helperInheritedChildCommand() *exec.Cmd {
	command := exec.Command(os.Args[0], daemonArgument) // #nosec G204 -- exact current helper executable and fixed argument.
	command.Env = append(os.Environ(), helperModeEnvironment+"=leaf")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command
}

func childPID(diagnostics []byte) (int, bool) {
	for line := range strings.SplitSeq(string(diagnostics), "\n") {
		value, found := strings.CutPrefix(line, "child=")
		if !found {
			continue
		}
		pid, err := strconv.Atoi(value)
		return pid, err == nil && pid > 0
	}
	return 0, false
}

func requireProcess(t *testing.T, candidate Candidate) *Process {
	t.Helper()
	process, ok := candidate.(*Process)
	if !ok {
		t.Fatalf("candidate = %T, want *Process", candidate)
	}
	return process
}

func waitHelperReady(t *testing.T, process *Process) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(string(process.ProtectedStderr()), "ready\n") {
			return
		}
		select {
		case <-process.Done():
			t.Fatalf("helper exited before readiness: %v, stderr=%q", process.Result(), process.ProtectedStderr())
		default:
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("helper readiness timed out: stderr=%q", process.ProtectedStderr())
}

type stubLaunchedProcess struct {
	waitErr  error
	closeErr error
	inputErr error
}

func (stub *stubLaunchedProcess) Wait() error       { return stub.waitErr }
func (stub *stubLaunchedProcess) CloseInput() error { return stub.inputErr }
func (*stubLaunchedProcess) Terminate() error       { return nil }
func (*stubLaunchedProcess) Kill() error            { return nil }
func (stub *stubLaunchedProcess) Close() error      { return stub.closeErr }
