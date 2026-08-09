package installedacceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	cancellationScenarioEnvironment = "SPICE_AGENT_ACCEPTANCE_CANCELLATION_SCENARIO"
	cancellationProviderDirectory   = "SPICE_AGENT_ACCEPTANCE_PROVIDER_DIRECTORY"
	cancellationShellHelper         = "SPICE_AGENT_ACCEPTANCE_SHELL_HELPER"
	cancellationRecoveryText        = "cancellation-recovery-complete"
)

// TestInstalledCancellationBoundaries proves that cancellation entered through
// the real pipe-driven Bubble Tea program crosses the daemon boundary and
// interrupts each executable layer. Every case then reuses the same daemon to
// prove cancellation did not poison the engine or a one-slot runtime plugin.
func TestInstalledCancellationBoundaries(t *testing.T) {
	root := repositoryRoot(t)
	daemonBinary, terminalBinary := buildApplications(t, root)
	shellHelper := buildOfflineTestBinary(t, root, "cancelhelper", "./testdata/cancelhelper")
	pluginBinary := buildOfflineTestBinary(
		t, root, "spice-agent-distribution-fixture", "./testdata/runtimeplugin/go",
	)
	pluginDigest := fileSHA256(t, pluginBinary)

	cases := []struct {
		name     string
		scenario string
		cancel   string
		prepare  func(*testing.T, map[string]string, string)
		ready    func(*testing.T, *process, string) []*managedProcessWitness
		recovery string
	}{
		{
			name: "provider blocked receive", scenario: "provider", cancel: "\x1b",
			ready: func(t *testing.T, _ *process, directory string) []*managedProcessWitness {
				t.Helper()
				waitForScenarioFile(t, filepath.Join(directory, "provider.ready"), nil)
				return nil
			},
		},
		{
			name: "compiled shell process tree", scenario: "shell", cancel: "\x18",
			prepare: func(_ *testing.T, environment map[string]string, _ string) {
				environment[cancellationShellHelper] = shellHelper
			},
			ready: func(t *testing.T, terminal *process, directory string) []*managedProcessWitness {
				t.Helper()
				waitForScenarioFile(t, filepath.Join(directory, "shell.ready"), terminal)
				return []*managedProcessWitness{
					openScenarioProcessWitness(t, filepath.Join(directory, "root.pid")),
					openScenarioProcessWitness(t, filepath.Join(directory, "child.pid")),
				}
			},
		},
		{
			name: "runtime plugin slot", scenario: "plugin", cancel: "\x1b",
			prepare: func(_ *testing.T, environment map[string]string, _ string) {
				configureAcceptancePlugin(environment, pluginBinary, pluginDigest)
			},
			ready: func(t *testing.T, terminal *process, _ string) []*managedProcessWitness {
				t.Helper()
				terminal.waitForOutput(t, "block ready")
				return nil
			},
			recovery: "echo accepted",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newManagedFixture(t, daemonBinary, terminalBinary)
			scenarioDirectory := t.TempDir()
			environment := cloneEnvironment(fixture.environment)
			environment[cancellationProviderDirectory] = scenarioDirectory
			environment[cancellationScenarioEnvironment] = testCase.scenario
			if testCase.prepare != nil {
				testCase.prepare(t, environment, scenarioDirectory)
			}

			daemon := startProcess(t, daemonBinary, []string{"serve"}, environment)
			t.Cleanup(func() { daemon.stop(t, false) })
			metadata := waitForEndpoint(t, fixture.store, daemon, nil, "")
			assertManagedBuild(t, metadata)
			terminal := startProcess(
				t, terminalBinary, []string{"attach", "--endpoint", metadata.Address()}, environment,
			)
			t.Cleanup(func() { terminal.stop(t, false) })
			terminal.waitForOutput(t, "[READY]")

			submitInstalledPrompt(t, terminal, "cancel "+testCase.scenario)
			witnesses := testCase.ready(t, terminal, scenarioDirectory)
			for _, witness := range witnesses {
				t.Cleanup(func() {
					if err := witness.Close(); err != nil {
						t.Errorf("close cancellation witness: %v", err)
					}
				})
			}
			terminal.write(t, testCase.cancel)
			if testCase.scenario == "provider" {
				waitForScenarioFile(t, filepath.Join(scenarioDirectory, "provider.cancelled"), terminal)
			}
			for _, witness := range witnesses {
				waitForScenarioProcessExit(t, witness, terminal)
			}
			terminal.waitForOutput(t, "run.cancelled")
			assertCancelledFrame(t, terminal)
			daemon.assertRunning(t)

			submitInstalledPrompt(t, terminal, "recover "+testCase.scenario)
			if testCase.recovery != "" {
				terminal.waitForOutput(t, testCase.recovery)
			}
			terminal.waitForOutput(t, cancellationRecoveryText)
			terminal.waitForOutput(t, "Completed runs: 1")
			assertRecoveredFrame(t, terminal)
			daemon.assertRunning(t)

			terminal.stop(t, true)
			daemon.assertRunning(t)
			daemon.stop(t, true)
			waitForEndpointAbsence(t, fixture.store, daemon)
		})
	}
}

func submitInstalledPrompt(t *testing.T, terminal *process, prompt string) {
	t.Helper()
	terminal.write(t, prompt)
	terminal.waitForOutput(t, prompt)
	terminal.write(t, "\r")
}

func assertCancelledFrame(t *testing.T, terminal *process) {
	t.Helper()
	frame := terminal.latestFrame()
	if !strings.Contains(frame, "run.cancelled") || strings.Contains(frame, "run.failed") ||
		strings.Contains(frame, "run.completed") || !strings.Contains(frame, "Completed runs: 0") {
		t.Fatalf("cancelled terminal frame violated terminal-event invariants:\n%s", frame)
	}
}

func assertRecoveredFrame(t *testing.T, terminal *process) {
	t.Helper()
	frame := terminal.latestFrame()
	if !strings.Contains(frame, cancellationRecoveryText) || !strings.Contains(frame, "Completed runs: 1") ||
		strings.Contains(frame, "Completed runs: 2") || strings.Contains(frame, "run.failed") {
		t.Fatalf("recovered terminal frame violated reuse invariants:\n%s", frame)
	}
}

func configureAcceptancePlugin(environment map[string]string, executable, digest string) {
	environment["SPICE_AGENT_RUNTIME_PLUGIN_REQUIRED"] = "true"
	environment["SPICE_AGENT_RUNTIME_PLUGIN_ID"] = "installed-cancellation-fixture"
	environment["SPICE_AGENT_RUNTIME_PLUGIN_PATH"] = executable
	environment["SPICE_AGENT_RUNTIME_PLUGIN_SHA256"] = digest
	environment["SPICE_AGENT_RUNTIME_PLUGIN_MANIFEST_NAME"] = "spice-agent-distribution-fixture"
	environment["SPICE_AGENT_RUNTIME_PLUGIN_MANIFEST_VERSION"] = "v1"
	environment["SPICE_AGENT_RUNTIME_PLUGIN_STARTUP_TIMEOUT"] = "5s"
	environment["SPICE_AGENT_RUNTIME_PLUGIN_CALL_TIMEOUT"] = "5s"
	environment["SPICE_AGENT_RUNTIME_PLUGIN_DRAIN_TIMEOUT"] = "5s"
	environment["SPICE_AGENT_RUNTIME_PLUGIN_SHUTDOWN_TIMEOUT"] = "5s"
	environment["SPICE_AGENT_RUNTIME_PLUGIN_CONTAINMENT_TIMEOUT"] = "5s"
}

func buildOfflineTestBinary(t *testing.T, root, name, target string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), executableName(name))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext( // #nosec G204 -- exact Go toolchain and fixed repository target.
		ctx, installedGoExecutable(), "build", "-mod=vendor", "-trimpath", "-buildvcs=false",
		"-ldflags=-buildid=", "-o", path, target,
	)
	command.Dir = root
	command.Env = mergedEnvironment(map[string]string{
		"GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local",
	})
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build offline %s: %v\n%s", target, err, output)
	}
	return path
}

func installedGoExecutable() string {
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(runtime.GOROOT(), "bin", name) //nolint:staticcheck // Exact executing toolchain.
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path) // #nosec G304 -- exact test-built executable path.
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func waitForScenarioFile(t *testing.T, path string, owner *process) {
	t.Helper()
	var lastErr error
	waitFor(t, func() bool {
		_, lastErr = os.Stat(path)
		return lastErr == nil
	}, func() string {
		detail := fmt.Sprintf("scenario marker %q was not written: %v", path, lastErr)
		if owner != nil {
			detail += fmt.Sprintf("\nstdout:\n%s\nstderr:\n%s", owner.stdout.String(), owner.stderr.String())
		}
		return detail
	})
}

func openScenarioProcessWitness(t *testing.T, path string) *managedProcessWitness {
	t.Helper()
	content, err := os.ReadFile(path) // #nosec G304 -- exact test-owned PID marker.
	if err != nil {
		t.Fatalf("read cancellation PID %q: %v", path, err)
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(string(content)), 10, 32)
	if err != nil || pid == 0 {
		t.Fatalf("decode cancellation PID %q: %v", content, err)
	}
	witness, err := openManagedProcessWitness(uint32(pid))
	if err != nil {
		t.Fatalf("observe cancellation process %d: %v", pid, err)
	}
	return witness
}

func waitForScenarioProcessExit(t *testing.T, witness *managedProcessWitness, owner *process) {
	t.Helper()
	var lastErr error
	waitFor(t, func() bool {
		var exited bool
		exited, lastErr = witness.Exited()
		return lastErr == nil && exited
	}, func() string {
		return fmt.Sprintf(
			"cancelled process remained alive: %v\nterminal stdout:\n%s\nterminal stderr:\n%s",
			lastErr, owner.stdout.String(), owner.stderr.String(),
		)
	})
}
