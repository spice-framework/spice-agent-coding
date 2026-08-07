package processplatform

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	agentprocess "github.com/spice-framework/spice-agent/process"
	"github.com/spice-framework/spice-agent/tool"
)

const (
	helperModeKey          = "SPICE_PROCESSPLATFORM_HELPER_MODE"
	helperValueKey         = "SPICE_PROCESSPLATFORM_HELPER_VALUE"
	helperReservedExitCode = 0x53504350
)

func TestProcessPlatformHelper(t *testing.T) {
	mode := os.Getenv(helperModeKey)
	if mode == "" {
		return
	}
	switch mode {
	case "early":
		return
	case "reserved-exit":
		os.Exit(helperReservedExitCode)
	case "echo":
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(91)
		}
		working, err := os.Getwd()
		if err != nil {
			os.Exit(92)
		}
		if _, err = fmt.Fprintf(os.Stdout, "cwd=%s\nvalue=%s\ninput=%s\nargs=%q\n",
			working, os.Getenv(helperValueKey), input, os.Args); err != nil {
			os.Exit(95)
		}
		_, _ = fmt.Fprintln(os.Stderr, "exact-stderr")
	case "blocked", "leaf":
		for {
			time.Sleep(time.Hour)
		}
	case "tree-exit":
		command := exec.Command(os.Args[0], append(helperTestArguments(), "--", "leaf")...) // #nosec G204 -- exact current helper and fixed arguments.
		command.Env = helperEnvironment("leaf")
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		if err := command.Start(); err != nil {
			os.Exit(93)
		}
		if _, err := fmt.Fprintf(os.Stdout, "child=%d\n", command.Process.Pid); err != nil {
			os.Exit(96)
		}
		time.Sleep(75 * time.Millisecond)
	default:
		os.Exit(94)
	}
}

func TestResolverUsesOnlyExplicitLookupState(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	toolPath := installProcessHelper(t, bin, "go")
	gitPath := installProcessHelper(t, bin, "git")
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(filepath.Join(bin, "go.cmd"), []byte("private"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{"PATH=bin"}
	if runtime.GOOS == "windows" {
		environment = append(environment, "PATHEXT=.CMD;.EXE")
	}
	resolver := NewResolver()
	for requested, want := range map[string]string{
		"go":  goCanonical(t, toolPath),
		"git": goCanonical(t, gitPath),
		filepath.Join("bin", filepath.Base(toolPath)): goCanonical(t, toolPath),
		toolPath: goCanonical(t, toolPath),
	} {
		lookup, err := agentprocess.NewLookup(requested, root, environment)
		if err != nil {
			t.Fatal(err)
		}
		got, err := resolver.Resolve(t.Context(), lookup)
		if err != nil || got != want || !filepath.IsAbs(got) || filepath.Clean(got) != got {
			t.Fatalf("Resolve(%q) = %q, %v; want %q", requested, got, err, want)
		}
	}

	ambient := t.TempDir()
	t.Setenv("PATH", ambient)
	missing, err := agentprocess.NewLookup("ambient-only", root, []string{"PATH="})
	if err != nil {
		t.Fatal(err)
	}
	if resolved, resolveErr := resolver.Resolve(t.Context(), missing); resolveErr == nil || resolved != "" {
		t.Fatalf("ambient lookup = %q, %v", resolved, resolveErr)
	}
	if runtime.GOOS == "windows" {
		script, lookupErr := agentprocess.NewLookup(filepath.Join("bin", "go.cmd"), root, environment)
		if lookupErr != nil {
			t.Fatal(lookupErr)
		}
		if resolved, resolveErr := resolver.Resolve(t.Context(), script); resolveErr == nil || resolved != "" {
			t.Fatalf("native resolver accepted shell script = %q, %v", resolved, resolveErr)
		}
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	lookup, err := agentprocess.NewLookup("go", root, environment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(canceled, lookup); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled resolution = %v", err)
	}
	for _, rendered := range []string{fmt.Sprint(resolver), fmt.Sprintf("%#v", resolver), resolver.LogValue().String()} {
		if strings.Contains(rendered, root) || strings.Contains(rendered, toolPath) {
			t.Fatalf("resolver formatting leaked state: %q", rendered)
		}
	}
}

func TestLauncherPreservesExactProcessIntentWithoutShell(t *testing.T) {
	root := t.TempDir()
	executable := installProcessHelper(t, root, "fixture")
	var stdout, stderr bytes.Buffer
	injection := `; echo injected && $(private-command)`
	spec := helperSpec(t, executable, root, "echo", strings.NewReader("exact-stdin"), &stdout, &stderr,
		[]string{"--", injection})
	launcher := mustLauncher(t)
	owned, err := launcher.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, resultErr := owned.Result(); resultErr == nil {
		t.Fatal("running process exposed a stable result")
	}
	wait, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err = owned.Wait(wait); err != nil {
		t.Fatal(err)
	}
	outcome, err := owned.Result()
	if err != nil || !outcome.Successful() {
		t.Fatalf("outcome = %#v, %v", outcome, err)
	}
	output := stdout.String()
	for _, want := range []string{"cwd=" + root, "value=exact-value", "input=exact-stdin", injection} {
		if !strings.Contains(output, want) {
			t.Fatalf("stdout %q does not contain %q", output, want)
		}
	}
	if stderr.String() != "exact-stderr\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if strings.Contains(output, "injected\n") {
		t.Fatalf("injection-looking argument was interpreted by a shell: %q", output)
	}
	for _, rendered := range []string{fmt.Sprint(launcher), fmt.Sprintf("%#v", launcher), launcher.LogValue().String(), fmt.Sprint(owned)} {
		if strings.Contains(rendered, root) || strings.Contains(rendered, injection) {
			t.Fatalf("formatting leaked process state: %q", rendered)
		}
	}
}

func TestRootOutcomeIsSeparateFromTreeContainment(t *testing.T) {
	root := t.TempDir()
	executable := installProcessHelper(t, root, "tree")
	var stdout bytes.Buffer
	spec := helperSpec(t, executable, root, "tree-exit", strings.NewReader(""), &stdout, io.Discard, nil)
	owned, err := mustLauncher(t).Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	waitForDone(t, owned.Done())
	outcome, err := owned.Result()
	if err != nil || !outcome.Successful() {
		t.Fatalf("root outcome = %#v, %v", outcome, err)
	}
	childPID := parseChildPID(t, stdout.String())
	short, cancelShort := context.WithTimeout(t.Context(), 25*time.Millisecond)
	err = owned.Wait(short)
	cancelShort()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("containment wait with live child = %v", err)
	}
	if err = owned.ForceKill(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = owned.ForceKill(t.Context()); err != nil {
		t.Fatalf("idempotent force kill = %v", err)
	}
	joined, cancelJoin := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelJoin()
	if err = owned.Wait(joined); err != nil {
		t.Fatal(err)
	}
	assertPlatformProcessStopped(t, childPID)
}

func TestLauncherCancellationAndPartialOwnership(t *testing.T) {
	root := t.TempDir()
	executable := installProcessHelper(t, root, "blocked")
	spec := helperSpec(t, executable, root, "blocked", strings.NewReader(""), io.Discard, io.Discard, nil)
	launcher := mustLauncher(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if owned, err := launcher.Start(canceled, spec); !errors.Is(err, context.Canceled) || owned != nil {
		t.Fatalf("pre-canceled launch = %T, %v", owned, err)
	}

	owned, err := launcher.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err = owned.RequestStop(nil); err == nil { //nolint:staticcheck // Boundary deliberately verifies nil-context rejection.
		t.Fatal("nil stop context succeeded")
	}
	if err = owned.RequestStop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = owned.RequestStop(t.Context()); err != nil {
		t.Fatalf("idempotent stop = %v", err)
	}
	joined, cancelJoin := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelJoin()
	if err = owned.Wait(joined); err != nil {
		t.Fatal(err)
	}
	outcome, err := owned.Result()
	if err != nil || outcome.Kind() != agentprocess.OutcomeSignaled {
		t.Fatalf("stopped outcome = %#v, %v", outcome, err)
	}

	sentinel := errors.New("private partial launch detail")
	fixture := &contractProcess{done: closedProcessChannel()}
	partial := &Launcher{registrar: noopRegistrar{}, start: func(
		context.Context, agentprocess.Spec, ChildRegistrar,
	) (agentprocess.Process, error) {
		return fixture, sentinel
	}}
	returned, err := partial.Start(t.Context(), spec)
	if returned != fixture || !errors.Is(err, sentinel) {
		t.Fatalf("partial launch = %T, %v", returned, err)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatalf("partial launch leaked cause: %v", err)
	}
	empty := &Launcher{registrar: noopRegistrar{}, start: func(
		context.Context, agentprocess.Spec, ChildRegistrar,
	) (agentprocess.Process, error) {
		return nil, nil //nolint:nilnil // Boundary verifies defensive rejection of a broken platform adapter.
	}}
	if returned, err = empty.Start(t.Context(), spec); returned != nil || err == nil {
		t.Fatalf("empty platform launch = %T, %v", returned, err)
	}
}

func TestLauncherPassesExactImmutableSpecToPlatform(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "executable"+executableSuffix())
	stdin := strings.NewReader("input")
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	spec, err := agentprocess.NewSpec(agentprocess.Config{
		Executable: executable, Arguments: []string{"one", "two"}, WorkingDirectory: root,
		Environment: []string{"B=two", "A=one"}, Stdin: stdin, Stdout: stdout, Stderr: stderr,
		Capabilities: []tool.Capability{tool.CapabilityProcessExecute, tool.CapabilityFilesystemRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &contractProcess{done: closedProcessChannel()}
	called := false
	launcher := &Launcher{registrar: noopRegistrar{}, start: func(
		ctx context.Context,
		received agentprocess.Spec,
		registrar ChildRegistrar,
	) (agentprocess.Process, error) {
		called = true
		if ctx != t.Context() || registrar == nil || received.Executable() != executable ||
			received.WorkingDirectory() != root || received.Stdin() != stdin ||
			received.Stdout() != stdout || received.Stderr() != stderr ||
			strings.Join(received.Arguments(), "|") != "one|two" ||
			strings.Join(received.Environment(), "|") != "A=one|B=two" ||
			len(received.Capabilities()) != 2 {
			t.Fatalf("platform specification changed: %#v", received)
		}
		arguments := received.Arguments()
		arguments[0] = "mutated"
		if received.Arguments()[0] != "one" {
			t.Fatal("platform specification exposed mutable arguments")
		}
		return fixture, nil
	}}
	owned, err := launcher.Start(t.Context(), spec)
	if err != nil || owned != fixture || !called {
		t.Fatalf("platform launch = %T, %v, called=%t", owned, err, called)
	}
}

func TestLauncherRegistrationFailureTransfersProcessOwnership(t *testing.T) {
	root := t.TempDir()
	executable := installProcessHelper(t, root, "registration")
	spec := helperSpec(t, executable, root, "blocked", strings.NewReader(""), io.Discard, io.Discard, nil)
	registrationFailure := errors.New("private registration failure")
	registrar := &testRegistrar{failure: registrationFailure}
	launcher, err := NewLauncher(registrar)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := launcher.Start(t.Context(), spec)
	if owned == nil || !errors.Is(err, registrationFailure) || registrar.pid <= 0 {
		t.Fatalf("registered partial launch = %T, %v, pid=%d", owned, err, registrar.pid)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatalf("registration failure leaked cause: %v", err)
	}
	if killErr := owned.ForceKill(t.Context()); killErr != nil {
		t.Fatal(killErr)
	}
	joined, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if waitErr := owned.Wait(joined); waitErr != nil {
		t.Fatal(waitErr)
	}
	if launcher, constructorErr := NewLauncher(nil); constructorErr == nil || launcher != nil {
		t.Fatalf("nil registrar constructor = %v, %v", launcher, constructorErr)
	}
}

func TestLauncherStartFailureIsRedacted(t *testing.T) {
	root := t.TempDir()
	privatePath := filepath.Join(root, "private-secret-executable"+executableSuffix())
	spec := helperSpec(t, privatePath, root, "echo", strings.NewReader(""), io.Discard, io.Discard, nil)
	owned, err := mustLauncher(t).Start(t.Context(), spec)
	if err == nil || owned != nil {
		t.Fatalf("missing executable launch = %T, %v", owned, err)
	}
	if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "private") {
		t.Fatalf("launch failure leaked path: %v", err)
	}
}

func helperSpec(
	t *testing.T,
	executable, directory, mode string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	arguments []string,
) agentprocess.Spec {
	t.Helper()
	allArguments := append(helperTestArguments(), arguments...)
	spec, err := agentprocess.NewSpec(agentprocess.Config{
		Executable: executable, Arguments: allArguments, WorkingDirectory: directory,
		Environment: helperEnvironment(mode), Stdin: stdin, Stdout: stdout, Stderr: stderr,
		Capabilities: []tool.Capability{tool.CapabilityProcessExecute},
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func helperTestArguments() []string {
	arguments := []string{"-test.run=TestProcessPlatformHelper"}
	if coverage := flag.Lookup("test.gocoverdir"); coverage != nil && coverage.Value.String() != "" {
		arguments = append(arguments, "-test.gocoverdir="+coverage.Value.String())
	}
	return arguments
}

func mustLauncher(t *testing.T) *Launcher {
	t.Helper()
	launcher, err := NewLauncher(noopRegistrar{})
	if err != nil {
		t.Fatal(err)
	}
	return launcher
}

func helperEnvironment(mode string) []string {
	environment := []string{helperModeKey + "=" + mode, helperValueKey + "=exact-value"}
	if runtime.GOOS == "windows" {
		for _, name := range []string{"SYSTEMROOT", "WINDIR"} {
			if value, found := os.LookupEnv(name); found {
				environment = append(environment, name+"="+value)
			}
		}
	}
	return environment
}

func installProcessHelper(t *testing.T, directory, name string) string {
	t.Helper()
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, name+executableSuffix())
	source, err := os.Open(current)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close() //nolint:errcheck // Test cleanup owns the source.
	destination, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(destination, source); err != nil {
		closeErr := destination.Close()
		t.Fatalf("copy helper: %v; close: %v", err, closeErr)
	}
	if err = destination.Close(); err != nil {
		t.Fatal(err)
	}
	return target
}

func executableSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func goCanonical(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(resolved)
}

func waitForDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("root outcome timed out")
	}
}

func parseChildPID(t *testing.T, output string) int {
	t.Helper()
	for line := range strings.SplitSeq(output, "\n") {
		value, found := strings.CutPrefix(line, "child=")
		if !found {
			continue
		}
		pid, err := strconv.Atoi(value)
		if err == nil && pid > 0 {
			return pid
		}
	}
	t.Fatalf("child PID missing from %q", output)
	return 0
}

type contractProcess struct{ done <-chan struct{} }

type testRegistrar struct {
	failure error
	pid     int
}

type noopRegistrar struct{}

func (registrar *testRegistrar) Register(process *os.Process) error {
	registrar.pid = process.Pid
	return registrar.failure
}

func (noopRegistrar) Register(*os.Process) error { return nil }

func (fixture *contractProcess) Done() <-chan struct{} { return fixture.done }
func (*contractProcess) Result() (agentprocess.Outcome, error) {
	return agentprocess.NewUnknownOutcome(), nil
}
func (*contractProcess) RequestStop(context.Context) error { return nil }
func (*contractProcess) ForceKill(context.Context) error   { return nil }
func (*contractProcess) Wait(context.Context) error        { return nil }

func closedProcessChannel() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
