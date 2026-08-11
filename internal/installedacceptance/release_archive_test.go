//go:build spice_release_artifacts

package installedacceptance

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent-coding/internal/releaseinstallation"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

const ephemeralRunnerEnvironment = "SPICE_AGENT_EPHEMERAL_RUNNER"

var verifiedArtifactDirectory = flag.String(
	"spice-release-artifact-dir",
	"",
	"absolute directory containing the independently verified nine release subjects",
)

var releaseCandidateRoot = flag.String(
	"spice-release-candidate-root",
	"",
	"absolute repository root containing the candidate's canonical inert release metadata",
)

// TestVerifiedNativeReleaseArchive executes exact released bytes only after the
// caller has independently verified and supplied the complete subject set.
func TestVerifiedNativeReleaseArchive(t *testing.T) {
	if verifiedArtifactDirectory == nil || *verifiedArtifactDirectory == "" {
		t.Fatal("-spice-release-artifact-dir is required")
	}
	if releaseCandidateRoot == nil || *releaseCandidateRoot == "" {
		t.Fatal("-spice-release-candidate-root is required")
	}
	set, err := releaseinstallation.NewVerifier().VerifyCandidate(*releaseCandidateRoot, *verifiedArtifactDirectory)
	if err != nil {
		t.Fatalf("validate independently verified release subjects: %v", err)
	}
	extraction := filepath.Join(t.TempDir(), "Spice Agent π installed bytes")
	installRoot, err := set.ExtractNative(extraction, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("extract native release archive: %v", err)
	}
	daemonBinary := filepath.Join(installRoot, executableName("spice-agentd"))
	terminalBinary := filepath.Join(installRoot, executableName("spice-agent"))
	assertReleasedVersion(t, daemonBinary, "spice-agentd", set)
	assertReleasedVersion(t, terminalBinary, "spice-agent", set)

	store, environment := releaseProcessEnvironment(t)
	assertReleasedCheck(t, daemonBinary, environment)
	assertReleasedCheck(t, terminalBinary, environment)

	t.Run("explicit serve and attach", func(t *testing.T) {
		assertEndpointAbsent(t, store, nil)
		daemon := startProcess(t, daemonBinary, []string{"serve"}, environment)
		t.Cleanup(func() { daemon.stop(t, false) })
		metadata := waitForEndpoint(t, store, daemon, nil, "")
		assertReleasedServer(t, metadata, set)

		terminal := startProcess(
			t, terminalBinary, []string{"attach", "--endpoint", metadata.Address()}, environment,
		)
		t.Cleanup(func() { terminal.stop(t, false) })
		terminal.waitForOutput(t, "[READY]")
		terminal.stop(t, true)
		daemon.assertRunning(t)
		daemon.stop(t, true)
		assertEndpointAbsent(t, store, daemon)
	})

	t.Run("zero argument managed sibling", func(t *testing.T) {
		assertEndpointAbsent(t, store, nil)
		terminal := startProcess(t, terminalBinary, nil, environment)
		t.Cleanup(func() { terminal.stop(t, false) })
		terminal.waitForOutput(t, "[READY]")
		metadata := waitForManagedEndpoint(t, store, terminal)
		assertReleasedServer(t, metadata, set)
		if int(metadata.Process().ID()) == terminal.command.Process.Pid {
			t.Fatalf("managed release daemon reused terminal PID %d", terminal.command.Process.Pid)
		}
		witness, err := openManagedProcessWitness(metadata.Process().ID())
		if err != nil {
			t.Fatalf("observe managed release daemon: %v", err)
		}
		defer func() {
			if closeErr := witness.Close(); closeErr != nil {
				t.Errorf("close managed release witness: %v", closeErr)
			}
		}()

		terminal.stop(t, true)
		waitForManagedProcessExit(t, witness, metadata, terminal)
		assertEndpointAbsent(t, store, terminal)
	})
}

func releaseProcessEnvironment(t *testing.T) (*endpoint.Store, map[string]string) {
	t.Helper()
	environment := map[string]string{
		"OPENAI_API_KEY":                      "release-archive-check-only",
		"OPENAI_BASE_URL":                     "https://127.0.0.1:1/v1",
		"OPENAI_MODEL":                        "release-archive-model",
		"OPENAI_MAX_RETRIES":                  "0",
		"OPENAI_TIMEOUT":                      "2s",
		"SPICE_AGENT_TERMINAL_ACCESSIBLE":     "true",
		"SPICE_AGENT_WORKSPACE":               t.TempDir(),
		"SPICE_AGENT_RUNTIME_PLUGIN_REQUIRED": "false",
	}
	switch runtime.GOOS {
	case "windows":
		if os.Getenv(ephemeralRunnerEnvironment) != "1" {
			t.Fatalf("Windows release-byte execution requires %s=1 and an ephemeral runner", ephemeralRunnerEnvironment)
		}
		environment[ephemeralRunnerEnvironment] = "1"
	case "linux", "darwin":
		runtimeDirectory := filepath.Join(t.TempDir(), "xdg-runtime")
		if err := os.Mkdir(runtimeDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		configurationDirectory := filepath.Join(t.TempDir(), "xdg-config")
		if err := os.Mkdir(configurationDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_RUNTIME_DIR", runtimeDirectory)
		t.Setenv("XDG_CONFIG_HOME", configurationDirectory)
		environment["XDG_RUNTIME_DIR"] = runtimeDirectory
		environment["XDG_CONFIG_HOME"] = configurationDirectory
		if runtime.GOOS == "darwin" {
			home := filepath.Join(t.TempDir(), "home")
			if err := os.Mkdir(home, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			environment["HOME"] = home
		}
	default:
		t.Fatalf("release-byte execution is unsupported on %s", runtime.GOOS)
	}
	scope, err := endpoint.CurrentUserScope()
	if err != nil {
		t.Fatalf("resolve release endpoint scope: %v", err)
	}
	store, err := scope.OpenStore(10 * time.Millisecond)
	if err != nil {
		t.Fatalf("open release endpoint store: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	assertEndpointAbsent(t, store, nil)
	return store, environment
}

func assertReleasedVersion(
	t *testing.T,
	binary, component string,
	set *releaseinstallation.Set,
) {
	t.Helper()
	process := startProcess(t, binary, []string{"--version"}, map[string]string{})
	if err := waitReleasedProcess(t, process, filepath.Base(binary)+" --version"); err != nil {
		t.Fatalf("run released %s --version: %v\nstderr: %s", component, err, process.stderr.String())
	}
	want := component + " " + strings.TrimPrefix(set.Version(), "v") + " (" + set.Commit() + ")\n"
	if got := process.stdout.String(); got != want {
		t.Fatalf("released %s --version = %q, want %q", component, got, want)
	}
}

func assertReleasedCheck(t *testing.T, binary string, environment map[string]string) {
	t.Helper()
	process := startProcess(t, binary, []string{"--check"}, environment)
	if err := waitReleasedProcess(t, process, filepath.Base(binary)+" --check"); err != nil {
		t.Fatalf(
			"released %s --check failed: %v\nstdout:\n%s\nstderr:\n%s",
			filepath.Base(binary), err, process.stdout.String(), process.stderr.String(),
		)
	}
}

func waitReleasedProcess(t *testing.T, process *process, operation string) error {
	t.Helper()
	select {
	case <-process.done:
		return process.waitResult()
	case <-time.After(observationTimeout):
		process.stop(t, false)
		return fmt.Errorf(
			"%s timed out: stdout %q stderr %q",
			operation, process.stdout.String(), process.stderr.String(),
		)
	}
}

func assertReleasedServer(t *testing.T, metadata endpoint.Metadata, set *releaseinstallation.Set) {
	t.Helper()
	if metadata.Server().Version() != strings.TrimPrefix(set.Version(), "v") ||
		metadata.Server().Commit() != set.Commit() {
		t.Fatalf(
			"released daemon advertised %q %q, want %q %q",
			metadata.Server().Version(), metadata.Server().Commit(),
			strings.TrimPrefix(set.Version(), "v"), set.Commit(),
		)
	}
}

func assertEndpointAbsent(t *testing.T, store *endpoint.Store, owner *process) {
	t.Helper()
	_, err := store.Discover(t.Context())
	if errors.Is(err, endpoint.ErrNotFound) {
		return
	}
	detail := ""
	if owner != nil {
		detail = fmt.Sprintf("\nstdout:\n%s\nstderr:\n%s", owner.stdout.String(), owner.stderr.String())
	}
	t.Fatalf("release endpoint is not absent: %v%s", err, detail)
}
