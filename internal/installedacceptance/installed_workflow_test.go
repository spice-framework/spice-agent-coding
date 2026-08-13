package installedacceptance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

const (
	acceptanceAPIKey     = "installed-acceptance-secret"
	acceptanceVersion    = "0.1.0-preview.1-acceptance"
	acceptanceCommit     = "installed-acceptance-commit"
	workspaceMarker      = "installed-acceptance-workspace-marker"
	acceptanceTimeout    = 90 * time.Second
	observationTimeout   = 20 * time.Second
	distributionLinkPath = "github.com/spice-framework/spice-agent-coding/internal/distribution."
)

// TestInstalledDaemonAndTerminalReconnect proves the shipped generated
// applications as independent OS processes. It faults only the established
// terminal transport while the daemon and run stay alive, then replaces the
// daemon between runs while the same Bubble Tea process preserves its local
// activity and prompt history.
func TestInstalledDaemonAndTerminalReconnect(t *testing.T) {
	root := repositoryRoot(t)
	currentScope, err := endpoint.CurrentUserScope()
	if err != nil {
		t.Fatal(err)
	}
	scopeDirectory := filepath.Join(
		currentScope.Directory(),
		fmt.Sprintf("installed-acceptance-%d-%d", os.Getpid(), time.Now().UnixNano()),
	)
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(scopeDirectory); removeErr != nil { // #nosec G703 -- exact test-owned child of the validated user scope.
			t.Error(removeErr)
		}
	})
	store, err := endpoint.OpenStore(endpoint.StoreConfig{
		Directory: scopeDirectory, PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if _, discoverErr := store.Discover(t.Context()); !errors.Is(discoverErr, endpoint.ErrNotFound) {
		t.Fatalf("inspect acceptance endpoint: %v", discoverErr)
	}

	workspace := t.TempDir()
	if err = os.WriteFile(filepath.Join(workspace, "README.md"), []byte(workspaceMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	authority := filepath.Join(
		scopeDirectory,
		fmt.Sprintf("installed-acceptance-authority-%d-%d", os.Getpid(), time.Now().UnixNano()),
	)
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(authority); removeErr != nil { // #nosec G703 -- exact test-owned child of the validated user scope.
			t.Error(removeErr)
		}
	})
	providerDirectory := t.TempDir()
	daemonBinary, terminalBinary := buildApplications(t, root)
	assertVersion(t, daemonBinary, "spice-agentd")
	assertVersion(t, terminalBinary, "spice-agent")
	faultTrigger := filepath.Join(t.TempDir(), "fault.trigger")
	faultAck := filepath.Join(t.TempDir(), "fault.ack")
	faultDiagnostic := filepath.Join(t.TempDir(), "daemon.diagnostic")

	baseEnvironment := map[string]string{
		"GOWORK":                                    "off",
		"OPENAI_API_KEY":                            acceptanceAPIKey,
		"OPENAI_MODEL":                              "installed-acceptance-model",
		"OPENAI_MAX_RETRIES":                        "0",
		"OPENAI_TIMEOUT":                            "30s",
		"SPICE_AGENT_WORKSPACE":                     workspace,
		"SPICE_AGENT_RUN_AUTHORITY_DIRECTORY":       authority,
		"SPICE_AGENT_ACCEPTANCE_SCOPE_DIRECTORY":    scopeDirectory,
		"SPICE_AGENT_ACCEPTANCE_PROVIDER_DIRECTORY": providerDirectory,
		"SPICE_AGENT_ACCEPTANCE_RESPONSE_PREFIX":    "checkpoint",
	}
	daemonEnvironment := cloneEnvironment(baseEnvironment)
	daemonEnvironment["SPICE_AGENT_ACCEPTANCE_FAULT_TRIGGER"] = faultTrigger
	daemonEnvironment["SPICE_AGENT_ACCEPTANCE_FAULT_ACK"] = faultAck
	daemonEnvironment["SPICE_AGENT_ACCEPTANCE_DIAGNOSTIC"] = faultDiagnostic

	daemonOne := startProcess(t, daemonBinary, []string{"serve"}, daemonEnvironment)
	t.Cleanup(func() { daemonOne.stop(t, false) })
	metadataOne := waitForEndpoint(t, store, daemonOne, nil, faultDiagnostic)
	assertIsolatedEndpoint(t, currentScope, scopeDirectory, metadataOne)
	if metadataOne.Server().Version() != acceptanceVersion ||
		metadataOne.Server().Commit() != acceptanceCommit {
		t.Fatalf(
			"daemon advertised build = %q %q",
			metadataOne.Server().Version(), metadataOne.Server().Commit(),
		)
	}

	terminalEnvironment := cloneEnvironment(baseEnvironment)
	terminalEnvironment["SPICE_AGENT_TERMINAL_ACCESSIBLE"] = "true"
	terminal := startProcess(
		t,
		terminalBinary,
		[]string{"attach", "--endpoint", metadataOne.Address()},
		terminalEnvironment,
	)
	t.Cleanup(func() { terminal.stop(t, true) })
	terminal.waitForOutput(t, "[READY]")

	terminal.write(t, "first installed prompt")
	terminal.waitForOutput(t, "first installed prompt")
	terminal.write(t, "\r")
	waitForFile(t, filepath.Join(providerDirectory, "checkpoint"))
	terminal.waitForOutput(t, "checkpoint-one")
	if err = os.WriteFile(faultTrigger, []byte("fault\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, faultAck)
	if err = os.WriteFile(filepath.Join(providerDirectory, "release"), []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	terminal.waitForOutput(t, "checkpoint-two")
	terminal.waitForOutput(t, "event stream reconnected after sequence")
	terminal.waitForOutput(t, "Completed runs: 1")
	waitFor(t, func() bool {
		return strings.Contains(daemonOne.stderr.String(), `"event":"agent.run.completed"`)
	}, func() string {
		return "daemon one did not emit its structured run completion:\n" + daemonOne.stderr.String()
	})
	if strings.Contains(terminal.stdout.String(), "Completed runs: 2") {
		t.Fatal("the first run produced more than one terminal event")
	}

	firstFrame := terminal.latestFrame()
	if !strings.Contains(firstFrame, "Completed runs: 1") {
		t.Fatalf("first terminal state did not expose one completed run\n%s", firstFrame)
	}

	daemonOne.stop(t, false)
	if err = os.Remove(faultTrigger); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err = os.Remove(faultAck); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	secondEnvironment := cloneEnvironment(baseEnvironment)
	secondProviderDirectory := t.TempDir()
	secondEnvironment["SPICE_AGENT_ACCEPTANCE_PROVIDER_DIRECTORY"] = secondProviderDirectory
	secondEnvironment["SPICE_AGENT_ACCEPTANCE_RESPONSE_PREFIX"] = "replacement"
	daemonTwo := startProcess(t, daemonBinary, []string{"serve"}, secondEnvironment)
	t.Cleanup(func() { daemonTwo.stop(t, true) })
	metadataTwo := waitForEndpoint(t, store, daemonTwo, &metadataOne, "")
	assertIsolatedEndpoint(t, currentScope, scopeDirectory, metadataTwo)
	if metadataTwo.Process().ID() == metadataOne.Process().ID() &&
		bytes.Equal(metadataTwo.Process().InstanceID(), metadataOne.Process().InstanceID()) {
		t.Fatal("replacement daemon reused the prior process identity")
	}
	terminal.waitForOutput(t, "daemon connection restored with a fresh session")
	terminal.assertRunning(t)

	terminal.write(t, "second installed prompt")
	terminal.waitForOutput(t, "second installed prompt")
	terminal.write(t, "\r")
	waitForFile(t, filepath.Join(secondProviderDirectory, "checkpoint"))
	if err = os.WriteFile(filepath.Join(secondProviderDirectory, "release"), []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	terminal.waitForOutput(t, "replacement-complete")
	terminal.waitForOutput(t, "Completed runs: 2")
	waitFor(t, func() bool {
		return strings.Contains(daemonTwo.stderr.String(), `"event":"agent.run.completed"`)
	}, func() string {
		return "daemon two did not emit its structured run completion:\n" + daemonTwo.stderr.String()
	})

	// Bubble Tea remains the same process and retains both prompt-history items
	// across daemon replacement. Two native history-up actions reach the first.
	secondPromptCount := strings.Count(terminal.stdout.String(), "second installed prompt")
	terminal.write(t, "\x1b[A")
	terminal.waitForOutputCount(t, "second installed prompt", secondPromptCount+1)
	firstPromptCount := strings.Count(terminal.stdout.String(), "first installed prompt")
	terminal.write(t, "\x1b[A")
	terminal.waitForOutputCount(t, "first installed prompt", firstPromptCount+1)

	finalFrame := terminal.latestFrame()
	if !strings.Contains(finalFrame, "Completed runs: 2") {
		t.Fatalf("final terminal state did not expose two completed runs\n%s", finalFrame)
	}
	if strings.Contains(terminal.stdout.String(), "Completed runs: 3") {
		t.Fatal("replay duplicated a terminal run event")
	}

	terminal.stop(t, true)
	daemonTwo.stop(t, true)
	assertInstalledLogging(t, daemonOne, daemonTwo, terminal, workspace, providerDirectory, secondProviderDirectory)
}

func assertInstalledLogging(
	t *testing.T,
	daemonOne *process,
	daemonTwo *process,
	terminal *process,
	workspace string,
	providerDirectories ...string,
) {
	t.Helper()
	for name, daemonProcess := range map[string]*process{"first": daemonOne, "second": daemonTwo} {
		logs := daemonProcess.stderr.String()
		if !strings.Contains(logs, `"schema":"spice.log/v1"`) ||
			strings.Count(logs, `"event":"agent.run.completed"`) != 1 {
			t.Fatalf("%s daemon structured run logs are incomplete or duplicated:\n%s", name, logs)
		}
		for _, forbidden := range append([]string{
			acceptanceAPIKey,
			workspace,
			workspaceMarker,
			"first installed prompt",
			"second installed prompt",
			"checkpoint-one",
			"checkpoint-two",
			"replacement-complete",
		}, providerDirectories...) {
			if strings.Contains(logs, forbidden) {
				t.Fatalf("%s daemon structured logs exposed forbidden value %q", name, forbidden)
			}
		}
	}
	if terminalLogs := terminal.stderr.String(); strings.Contains(terminalLogs, "spice.log/v1") ||
		strings.Contains(terminalLogs, `"event":"agent.`) {
		t.Fatalf("terminal duplicated daemon Agent logs or corrupted TUI stderr:\n%s", terminalLogs)
	}
}

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(value)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

type process struct {
	command  *exec.Cmd
	stdin    io.WriteCloser
	stdout   *synchronizedBuffer
	stderr   *synchronizedBuffer
	done     chan struct{}
	result   error
	resultMu sync.Mutex
	once     sync.Once
}

func startProcess(t *testing.T, binary string, arguments []string, environment map[string]string) *process {
	t.Helper()
	return startProcessWithEnvironment(t, binary, arguments, mergedEnvironment(environment))
}

func startProcessWithEnvironment(
	t *testing.T,
	binary string,
	arguments []string,
	environment []string,
) *process {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), acceptanceTimeout)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, binary, arguments...) // #nosec G204 -- test-built binary and fixed acceptance arguments.
	command.Env = environment
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	result := &process{
		command: command,
		stdin:   stdin,
		stdout:  &synchronizedBuffer{},
		stderr:  &synchronizedBuffer{},
		done:    make(chan struct{}),
	}
	command.Stdout = result.stdout
	command.Stderr = result.stderr
	if err = command.Start(); err != nil {
		t.Fatalf("start %s: %v", filepath.Base(binary), err)
	}
	go func() {
		waitErr := command.Wait()
		result.resultMu.Lock()
		result.result = waitErr
		result.resultMu.Unlock()
		close(result.done)
	}()
	return result
}

func (process *process) write(t *testing.T, value string) {
	t.Helper()
	if _, err := io.WriteString(process.stdin, value); err != nil {
		t.Fatalf("write process input: %v\nstderr: %s", err, process.stderr.String())
	}
}

func (process *process) stop(t *testing.T, graceful bool) {
	t.Helper()
	process.once.Do(func() {
		if graceful {
			if _, err := process.stdin.Write([]byte{0x11}); err != nil && !errors.Is(err, os.ErrClosed) {
				t.Errorf("request graceful process shutdown: %v", err)
			}
			if err := process.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				t.Errorf("close process input: %v", err)
			}
		} else if process.command.Process != nil {
			if err := process.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("terminate process: %v", err)
			}
		}
		select {
		case <-process.done:
			err := process.waitResult()
			if graceful && err != nil {
				t.Errorf("process shutdown: %v\nstderr: %s", err, process.stderr.String())
			}
		case <-time.After(10 * time.Second):
			if process.command.Process != nil {
				if err := process.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					t.Errorf("terminate timed-out process: %v", err)
				}
			}
			t.Errorf("process did not stop\nstderr: %s", process.stderr.String())
		}
	})
}

func (process *process) waitForOutput(t *testing.T, expected string) {
	t.Helper()
	waitFor(t, func() bool { return strings.Contains(process.stdout.String(), expected) }, func() string {
		return fmt.Sprintf("stdout:\n%s\nstderr:\n%s", process.stdout.String(), process.stderr.String())
	})
}

func (process *process) waitForOutputCount(t *testing.T, expected string, count int) {
	t.Helper()
	waitFor(t, func() bool {
		return strings.Count(process.stdout.String(), expected) >= count
	}, func() string {
		return fmt.Sprintf("stdout:\n%s\nstderr:\n%s", process.stdout.String(), process.stderr.String())
	})
}

func (process *process) latestFrame() string {
	output := stripTerminalControl(process.stdout.String())
	index := strings.LastIndex(output, "Spice Agent — ")
	if index < 0 {
		return output
	}
	return output[index:]
}

func (process *process) assertRunning(t *testing.T) {
	t.Helper()
	select {
	case <-process.done:
		t.Fatalf("terminal process exited early: %v\nstderr: %s", process.waitResult(), process.stderr.String())
	default:
	}
}

func (process *process) waitResult() error {
	process.resultMu.Lock()
	defer process.resultMu.Unlock()
	return process.result
}

func buildApplications(t *testing.T, root string) (string, string) {
	t.Helper()
	directory := t.TempDir()
	daemon := filepath.Join(directory, executableName("spice-agentd"))
	terminal := filepath.Join(directory, executableName("spice-agent"))
	buildApplication(t, root, daemon, "./cmd/spice-agentd", "spice_acceptance")
	buildApplication(t, root, terminal, "./cmd/spice-agent", "spice_acceptance")
	return daemon, terminal
}

func buildApplication(t *testing.T, root, output, target, tags string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	linkerFlags := "-buildid= -X=" + distributionLinkPath + "Version=" + acceptanceVersion +
		" -X=" + distributionLinkPath + "Commit=" + acceptanceCommit
	arguments := []string{"build", "-mod=vendor", "-trimpath", "-buildvcs=false", "-ldflags=" + linkerFlags, "-o", output}
	if tags != "" {
		arguments = append(arguments, "-tags="+tags)
	}
	arguments = append(arguments, target)
	command := exec.CommandContext(ctx, "go", arguments...) // #nosec G204 -- exact Go command and fixed repository targets.
	command.Dir = root
	command.Env = mergedEnvironment(map[string]string{"GOWORK": "off"})
	if outputBytes, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", target, err, outputBytes)
	}
}

func assertVersion(t *testing.T, binary, component string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), binary, "--version") // #nosec G204 -- exact test-built executable and fixed argument.
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s --version: %v\n%s", component, err, output)
	}
	want := component + " " + acceptanceVersion + " (" + acceptanceCommit + ")\n"
	if string(output) != want {
		t.Fatalf("%s --version = %q, want %q", component, output, want)
	}
}

func waitForEndpoint(
	t *testing.T,
	store *endpoint.Store,
	daemon *process,
	previous *endpoint.Metadata,
	diagnosticPath string,
) endpoint.Metadata {
	t.Helper()
	var result endpoint.Metadata
	waitFor(t, func() bool {
		metadata, err := store.Discover(t.Context())
		if err != nil || int(metadata.Process().ID()) != daemon.command.Process.Pid {
			return false
		}
		if previous != nil && bytes.Equal(metadata.Process().InstanceID(), previous.Process().InstanceID()) {
			return false
		}
		result = metadata
		return true
	}, func() string {
		diagnostic := ""
		if diagnosticPath != "" {
			content, readErr := os.ReadFile(diagnosticPath) // #nosec G304 -- test-owned temporary diagnostic path.
			if readErr == nil {
				diagnostic = string(content)
			} else if !errors.Is(readErr, os.ErrNotExist) {
				diagnostic = "read diagnostic: " + readErr.Error()
			}
		}
		return fmt.Sprintf(
			"daemon did not publish its protected local endpoint\nstdout:\n%s\nstderr:\n%s\ndiagnostic:\n%s",
			daemon.stdout.String(), daemon.stderr.String(), diagnostic,
		)
	})
	return result
}

func assertIsolatedEndpoint(
	t *testing.T,
	current endpoint.UserScope,
	directory string,
	metadata endpoint.Metadata,
) {
	t.Helper()
	if metadata.Transport() != current.Transport() {
		t.Fatalf("isolated transport = %q, want %q", metadata.Transport(), current.Transport())
	}
	if metadata.Address() == current.Address() {
		t.Fatalf("acceptance daemon published the production user endpoint %q", metadata.Address())
	}
	switch current.Transport() {
	case endpoint.TransportUnixSocket:
		if filepath.Dir(metadata.Address()) != directory {
			t.Fatalf("isolated Unix endpoint %q is outside %q", metadata.Address(), directory)
		}
	case endpoint.TransportWindowsNamedPipe:
		if !strings.HasPrefix(metadata.Address(), `\\.\pipe\spice-agent-acceptance-`) {
			t.Fatalf("isolated Windows endpoint = %q", metadata.Address())
		}
	default:
		t.Fatalf("unsupported acceptance transport %q", current.Transport())
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	waitFor(t, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}, func() string { return "fault acknowledgement was not written" })
}

func waitFor(t *testing.T, condition func() bool, detail func() string) {
	t.Helper()
	deadline := time.Now().Add(observationTimeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("acceptance condition timed out: %s", detail())
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}

func executableName(value string) string {
	if runtime.GOOS == "windows" {
		return value + ".exe"
	}
	return value
}

func cloneEnvironment(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	maps.Copy(result, values)
	return result
}

func mergedEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, value := range os.Environ() {
		key, current, found := strings.Cut(value, "=")
		if found {
			values[strings.ToUpper(key)] = current
		}
	}
	for key, value := range overrides {
		values[strings.ToUpper(key)] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func stripTerminalControl(value string) string {
	var result strings.Builder
	for index := 0; index < len(value); {
		if value[index] == 0x1b && index+1 < len(value) && value[index+1] == '[' {
			index += 2
			for index < len(value) {
				character := value[index]
				index++
				if character >= 0x40 && character <= 0x7e {
					break
				}
			}
			continue
		}
		result.WriteByte(value[index])
		index++
	}
	return result.String()
}
