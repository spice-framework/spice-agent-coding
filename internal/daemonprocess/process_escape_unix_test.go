//go:build linux || darwin

package daemonprocess

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent-coding/internal/testpath"

	"golang.org/x/sys/unix"
)

const (
	unixHelperModeEnvironment   = "SPICE_DAEMONPROCESS_UNIX_HELPER_MODE"
	skipRootRegistryEnvironment = "SPICE_DAEMONPROCESS_SKIP_ROOT_REGISTRY"
)

func TestMain(testingMain *testing.M) {
	unixMode := os.Getenv(unixHelperModeEnvironment)
	genericMode, genericHelper := lastEnvironmentValue(os.Environ(), helperModeEnvironment)
	if genericHelper && unixMode == "" {
		mode := genericMode
		if err := os.Setenv(helperModeEnvironment, mode); err != nil {
			os.Exit(88)
		}
	}
	var registry *DescendantRegistry
	if _, managed := os.LookupEnv(descendantRegistryEnvironment); managed && os.Getenv(skipRootRegistryEnvironment) != "1" {
		opened, err := NewDescendantRegistry()
		if err == nil {
			registry = opened
		} else {
			// Helper descendants can inherit the non-secret environment marker
			// after the daemon root made its descriptor close-on-exec. They are
			// not daemon roots and must not retry against a reused descriptor.
			if err = os.Unsetenv(descendantRegistryEnvironment); err != nil {
				os.Exit(89)
			}
		}
	}
	if unixMode != "" {
		os.Exit(runUnixHelper(unixMode, registry))
	}
	if genericHelper {
		if genericMode == "orphan" {
			os.Exit(runRegisteredOrphan(registry, true))
		}
		os.Exit(runGenericHelper(genericMode))
	}
	os.Exit(testingMain.Run())
}

func runGenericHelper(mode string) int {
	_, _ = fmt.Fprintln(os.Stderr, "ready")
	switch mode {
	case "eof":
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			return 94
		}
	case "early":
		return 17
	case "blocked":
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			return 94
		}
		for {
			time.Sleep(time.Hour)
		}
	case "report":
		working, err := os.Getwd()
		if err != nil {
			return 95
		}
		_, _ = fmt.Fprintf(
			os.Stderr, "%s\ncwd=%s\nvalue=%s\n",
			strings.Repeat("x", 512), working, os.Getenv(helperValueEnvironment),
		)
		if _, err = io.Copy(io.Discard, os.Stdin); err != nil {
			return 94
		}
	case "tree":
		command := helperChildCommand()
		if err := command.Start(); err != nil {
			return 92
		}
		_, _ = fmt.Fprintf(os.Stderr, "child=%d\n", command.Process.Pid)
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			return 94
		}
		for {
			time.Sleep(time.Hour)
		}
	case "leaf":
		for {
			time.Sleep(time.Hour)
		}
	default:
		return 93
	}
	return 0
}

func runRegisteredOrphan(registry *DescendantRegistry, inheritDiagnostics bool) int {
	if registry == nil {
		return 83
	}
	command := escapedLeafCommand()
	if inheritDiagnostics {
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
	}
	launch, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := registry.Start(launch, command)
	cancel()
	if err != nil {
		return 84
	}
	_, _ = fmt.Fprintf(os.Stderr, "child=%d\nready\n", command.Process.Pid)
	return 0
}

func lastEnvironmentValue(environment []string, name string) (string, bool) {
	prefix := name + "="
	for _, entry := range slices.Backward(environment) {
		if value, found := strings.CutPrefix(entry, prefix); found {
			return value, true
		}
	}
	return "", false
}

func runUnixHelper(mode string, registry *DescendantRegistry) int {
	if len(os.Args) < 2 || os.Args[1] != daemonArgument {
		return 81
	}
	if _, gated := os.LookupEnv(descendantGateEnvironment); gated {
		if err := (DescendantRegistration{}).Await(); err != nil {
			return 87
		}
	}
	switch mode {
	case "escaped-leaf":
		for {
			time.Sleep(time.Hour)
		}
	case "escaped-root":
		child, err := startEscapedLeaf()
		if err != nil {
			return 82
		}
		_, _ = fmt.Fprintf(os.Stderr, "child=%d\nready\n", child.Pid)
		if _, err = io.Copy(io.Discard, os.Stdin); err != nil {
			return 94
		}
		for {
			time.Sleep(time.Hour)
		}
	case "registered-root":
		return runRegisteredOrphan(registry, false)
	default:
		return 86
	}
}

func startEscapedLeaf() (*os.Process, error) {
	command := escapedLeafCommand()
	if err := command.Start(); err != nil {
		return nil, err
	}
	return command.Process, nil
}

func escapedLeafCommand() *exec.Cmd {
	command := exec.Command(os.Args[0], daemonArgument) // #nosec G204 -- exact helper executable and fixed argument.
	command.Env = replaceEnvironment(os.Environ(), unixHelperModeEnvironment, "escaped-leaf")
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return command
}

func TestEscapedProcessGroupDescendantIsKilled(t *testing.T) {
	starter := unixHelperStarter(t, "escaped-root")
	starter.graceful = 20 * time.Millisecond
	candidate := requireOwnedCandidate(t, starter)
	process := requireProcess(t, candidate)
	waitHelperReady(t, process)
	child, found := childPID(process.ProtectedStderr())
	if !found {
		t.Fatalf("helper did not report escaped child: %q", process.ProtectedStderr())
	}
	launched := requireUnixProcess(t, process.child)
	rootGroup, rootErr := unix.Getpgid(launched.rootPID)
	childGroup, childErr := unix.Getpgid(child)
	if rootErr != nil || childErr != nil || rootGroup == childGroup || childGroup != child {
		t.Fatalf("process groups root=%d child=%d rootErr=%v childErr=%v", rootGroup, childGroup, rootErr, childErr)
	}
	if err := candidate.BeginShutdown(); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	// The root is intentionally terminated after ignoring EOF; only its root
	// outcome is non-nil. Containment completion is observed through Done and
	// the escaped process check.
	if err := candidate.Wait(wait); err != nil || candidate.Result() == nil {
		t.Fatalf("terminated root cleanup/result = %v/%v", err, candidate.Result())
	}
	select {
	case <-candidate.Done():
	default:
		t.Fatal("managed daemon did not finish containment cleanup")
	}
	assertProcessStopped(t, child)
}

func TestRegisteredEscapedOrphanIsKilledAfterRootExit(t *testing.T) {
	starter := unixHelperStarter(t, "registered-root")
	candidate := requireOwnedCandidate(t, starter)
	process := requireProcess(t, candidate)
	waitHelperReady(t, process)
	child, found := childPID(process.ProtectedStderr())
	if !found {
		t.Fatalf("helper did not report registered child: %q", process.ProtectedStderr())
	}
	wait, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := candidate.Wait(wait); err != nil {
		t.Fatalf("registered containment failed: %v, root=%v", err, candidate.Result())
	}
	assertProcessStopped(t, child)
}

func TestMissingRootRegistryHandshakeFailsContainment(t *testing.T) {
	starter := unixHelperStarter(t, "")
	starter.environment = replaceEnvironment(starter.environment, helperModeEnvironment, "eof")
	starter.environment = replaceEnvironment(starter.environment, skipRootRegistryEnvironment, "1")
	candidate := requireOwnedCandidate(t, starter)
	process := requireProcess(t, candidate)
	waitHelperReady(t, process)
	if err := candidate.BeginShutdown(); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := candidate.Wait(wait); err == nil || candidate.Result() != nil {
		t.Fatalf("missing root handshake cleanup/root = %v/%v, want containment failure and nil root", err, candidate.Result())
	}
}

func TestRegistryStartRejectsCallerSelectedProcessGroup(t *testing.T) {
	deadline, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	registry := &DescendantRegistry{}
	command := exec.Command(os.Args[0], daemonArgument) // #nosec G204 -- validation happens before launch.
	command.SysProcAttr = &syscall.SysProcAttr{Pgid: 7}
	if err := registry.Start(deadline, command); err == nil {
		t.Fatal("registry accepted a caller-selected process group")
	}
}

func TestRegistryRejectsNonpositiveProcessIdentityBeforeWriting(t *testing.T) {
	registry := &DescendantRegistry{}
	for _, pid := range []int{0, -1} {
		if err := registry.exchangeLocked(pid); err == nil || !strings.Contains(err.Error(), "PID is invalid") {
			t.Fatalf("exchangeLocked(%d) error = %v", pid, err)
		}
	}
}

func TestAdoptRootRegistryExplicitServeAndMalformedManagedEndpoint(t *testing.T) {
	original, existed := os.LookupEnv(descendantRegistryEnvironment)
	if err := os.Unsetenv(descendantRegistryEnvironment); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var err error
		if existed {
			err = os.Setenv(descendantRegistryEnvironment, original)
		} else {
			err = os.Unsetenv(descendantRegistryEnvironment)
		}
		if err != nil {
			t.Errorf("restore registry environment: %v", err)
		}
	})
	registry, err := (RootRegistryFactory{}).Adopt()
	if err != nil {
		t.Fatal(err)
	}
	if _, inactive := registry.(inactiveRootRegistry); !inactive {
		t.Fatalf("explicit serve registry = %T, want inactive", registry)
	}
	if err = registry.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Setenv(descendantRegistryEnvironment, "malformed"); err != nil {
		t.Fatal(err)
	}
	if registry, err = (RootRegistryFactory{}).Adopt(); err == nil || registry != nil {
		t.Fatalf("malformed managed endpoint = %T, %v", registry, err)
	}
}

func unixHelperStarter(t *testing.T, mode string) *Starter {
	t.Helper()
	executable := installHelperExecutable(t)
	launcher := filepath.Join(filepath.Dir(executable), launcherExecutableName())
	environment := replaceEnvironment(os.Environ(), unixHelperModeEnvironment, mode)
	starter, err := newTestStarter(Config{
		Directory: testpath.TempDir(t), Environment: environment, StderrBytes: 1024,
		GracefulTimeout: 2 * time.Second, TerminateDelay: 100 * time.Millisecond,
	}, executable, launcher)
	if err != nil {
		t.Fatal(err)
	}
	return starter
}

func replaceEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func requireUnixProcess(t *testing.T, process launchedProcess) *unixLaunchedProcess {
	t.Helper()
	unixProcess, ok := process.(*unixLaunchedProcess)
	if !ok {
		t.Fatalf("launched process type = %T, want *unixLaunchedProcess", process)
	}
	return unixProcess
}
