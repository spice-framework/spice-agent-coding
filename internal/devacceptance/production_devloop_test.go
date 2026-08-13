package devacceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

const productionWorkspaceMarker = "installed-acceptance-workspace-marker"

type productionSupervisor struct {
	*developmentProcess
	input *os.File
}

// TestProductionSpiceDevSupervisorsPreserveTerminalAcrossDaemonReplacement
// proves the shipped daemon and terminal development targets against two real,
// simultaneous instances of the vendored Spice supervisor. Daemon-only source
// failures and replacement must never restart the terminal supervisor or its
// Bubble Tea candidate.
func TestProductionSpiceDevSupervisorsPreserveTerminalAcrossDaemonReplacement(t *testing.T) {
	if testing.Short() {
		t.Skip("real production development supervisors are process-heavy")
	}
	repository := repositoryRoot(t)
	scratch := t.TempDir()
	workspace := filepath.Join(scratch, "workspace")
	copyProductionDevelopmentWorkspace(t, repository, workspace)
	spiceExecutable := buildSpiceExecutable(t, repository, scratch)
	originalHashes := productionDevelopmentHashes(t, workspace)

	currentScope, err := endpoint.CurrentUserScope()
	if err != nil {
		t.Fatalf("resolve current-user scope: %v", err)
	}
	scopeDirectory := filepath.Join(
		currentScope.Directory(),
		fmt.Sprintf("coding-devacceptance-%d-%d", os.Getpid(), time.Now().UnixNano()),
	)
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(scopeDirectory); removeErr != nil { // #nosec G703 -- exact test-owned child of the validated current-user scope.
			t.Errorf("remove development endpoint scope: %v", removeErr)
		}
	})
	store, err := endpoint.OpenStore(endpoint.StoreConfig{
		Directory:    scopeDirectory,
		PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("open development endpoint store: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("close development endpoint store: %v", closeErr)
		}
	})
	if _, discoverErr := store.Discover(t.Context()); !errors.Is(discoverErr, endpoint.ErrNotFound) {
		t.Fatalf("initial development endpoint discovery = %v, want not found", discoverErr)
	}

	agentWorkspace := filepath.Join(scratch, "agent workspace")
	if err = os.MkdirAll(agentWorkspace, 0o750); err != nil {
		t.Fatalf("create agent workspace: %v", err)
	}
	if err = os.WriteFile(
		filepath.Join(agentWorkspace, "README.md"),
		[]byte(productionWorkspaceMarker+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write agent workspace marker: %v", err)
	}
	providerDirectory := filepath.Join(scratch, "provider")
	if err = os.MkdirAll(providerDirectory, 0o750); err != nil {
		t.Fatalf("create provider directory: %v", err)
	}
	authorityDirectory := filepath.Join(scopeDirectory, "authority")
	environment := productionDevelopmentEnvironment(map[string]string{
		"OPENAI_API_KEY":     "dual-supervisor-test-secret",
		"OPENAI_MAX_RETRIES": "0",
		"OPENAI_MODEL":       "dual-supervisor-model",
		"OPENAI_TIMEOUT":     "30s",
		"SPICE_AGENT_ACCEPTANCE_PROVIDER_DIRECTORY": providerDirectory,
		"SPICE_AGENT_ACCEPTANCE_RESPONSE_PREFIX":    "dual-supervisor",
		"SPICE_AGENT_ACCEPTANCE_SCOPE_DIRECTORY":    scopeDirectory,
		"SPICE_AGENT_RUN_AUTHORITY_DIRECTORY":       authorityDirectory,
		"SPICE_AGENT_TERMINAL_ACCESSIBLE":           "true",
		"SPICE_AGENT_WORKSPACE":                     agentWorkspace,
	})

	daemonOutput := newEventBuffer()
	daemonSupervisor := startProductionSupervisor(
		t, spiceExecutable, workspace, daemonDevelopmentArguments(), environment, daemonOutput,
	)
	t.Cleanup(func() { daemonSupervisor.cleanup(t) })
	daemonOutput.waitForText(t, "spice dev: application started (revision 1)")
	firstDaemon := waitForDevelopmentEndpoint(t, store, nil, daemonOutput)

	terminalOutput := newEventBuffer()
	terminalSupervisor := startProductionSupervisor(
		t, spiceExecutable, workspace, terminalDevelopmentArguments(), environment, terminalOutput,
	)
	t.Cleanup(func() { terminalSupervisor.cleanup(t) })
	terminalOutput.waitForText(t, "spice dev: application started (revision 1)")
	terminalOutput.waitForText(t, "[READY]")
	terminalIdentity := waitForDevelopmentApplicationIdentity(
		t, terminalSupervisor.command.Process.Pid, terminalOutput,
	)
	terminalSupervisorPID := terminalSupervisor.command.Process.Pid

	runProductionPrompt(t, terminalSupervisor, terminalOutput, providerDirectory, "first supervisor prompt", 1)
	waitForOutputCount(t, daemonOutput, `"event":"agent.run.completed"`, 1)
	removeProviderHandshake(t, providerDirectory)

	applicationPath := filepath.Join(workspace, "cmd", "spice-agentd", "application.go")
	originalApplication := readFile(t, applicationPath)
	brokenApplication := bytes.Replace(
		originalApplication,
		[]byte("// @Application"),
		[]byte("// @Application\n// @Unknown"),
		1,
	)
	if bytes.Equal(brokenApplication, originalApplication) {
		t.Fatal("daemon application marker was not found")
	}
	atomicReplace(t, scratch, applicationPath, brokenApplication)
	daemonOutput.waitForText(t, "spice dev: revision 2 failed:")
	daemonOutput.waitForText(t, "spice dev: application remains on last-known-good revision 1")
	assertDevelopmentEndpointIdentity(t, store, firstDaemon, daemonOutput)
	assertProductionTerminalUnchanged(
		t, terminalSupervisor, terminalSupervisorPID, terminalIdentity, terminalOutput,
	)

	replacementOffset := len(daemonOutput.String())
	atomicReplace(t, scratch, applicationPath, originalApplication)
	replacementRevision := daemonOutput.waitForApplicationRevision(t, replacementOffset, 3)
	daemonOutput.waitForText(t, fmt.Sprintf(
		"spice dev: graceful restart requested for revision %d", replacementRevision,
	))
	secondDaemon := waitForDevelopmentEndpoint(t, store, &firstDaemon, daemonOutput)
	if secondDaemon.Process().ID() == firstDaemon.Process().ID() &&
		bytes.Equal(secondDaemon.Process().InstanceID(), firstDaemon.Process().InstanceID()) {
		t.Fatal("valid daemon-only edit retained the prior daemon process identity")
	}
	beforeSecond := terminalOutput.Count("dual-supervisor-two")
	beginProductionPrompt(t, terminalSupervisor, terminalOutput, "second supervisor prompt")
	terminalOutput.waitForText(t, "daemon connection restored with a fresh session")
	assertProductionTerminalUnchanged(
		t, terminalSupervisor, terminalSupervisorPID, terminalIdentity, terminalOutput,
	)

	awaitProductionPrompt(t, terminalOutput, providerDirectory, 2, beforeSecond)
	waitForOutputCount(t, daemonOutput, `"event":"agent.run.completed"`, 2)
	assertProductionHistory(t, terminalSupervisor, terminalOutput)
	if count := strings.Count(daemonOutput.String(), `"event":"agent.run.completed"`); count != 2 {
		t.Fatalf("daemon completion log count = %d, want 2\n%s", count, diagnosticTail(daemonOutput.String()))
	}
	if strings.Contains(stripDevelopmentTerminalControl(terminalOutput.String()), "Completed runs: 3") {
		t.Fatalf("terminal replay duplicated a run\n%s", diagnosticTail(terminalOutput.String()))
	}
	assertProductionTerminalUnchanged(
		t, terminalSupervisor, terminalSupervisorPID, terminalIdentity, terminalOutput,
	)

	terminalSupervisor.write(t, "\x11")
	terminalOutput.waitForText(t, "spice dev: application exited (revision 1)")
	terminalSupervisor.stop(t)
	daemonSupervisor.stop(t)
	waitForDevelopmentEndpointAbsent(t, store, daemonOutput)
	if got := productionDevelopmentHashes(t, workspace); !equalHashes(got, originalHashes) {
		t.Fatalf("development workspace hashes changed:\n%s", hashDifference(originalHashes, got))
	}
}

func copyProductionDevelopmentWorkspace(t *testing.T, repository, workspace string) {
	t.Helper()
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		t.Fatalf("create production development workspace: %v", err)
	}
	copyFile(t, filepath.Join(repository, "go.mod"), filepath.Join(workspace, "go.mod"))
	copyFile(t, filepath.Join(repository, "go.sum"), filepath.Join(workspace, "go.sum"))
	for _, directory := range []string{".spice", "cmd", "internal", "vendor"} {
		copyTree(t, filepath.Join(repository, directory), filepath.Join(workspace, directory))
	}
}

func daemonDevelopmentArguments() []string {
	return []string{
		"dev", "--target", "spice-agentd",
		"--exclude=cmd/spice-agent/**",
		"--exclude=internal/terminal/**",
		"--exclude=internal/terminalcommand/**",
		"--exclude=internal/terminalconnector/**",
		"--exclude=internal/tuisession/**",
		"--exclude=internal/spicegen/spice_agent/**",
		"--exclude=.spice/spice_agent.manifest.json",
		"--exclude=internal/architectureproof/**",
		"./cmd/spice-agentd", "--", "serve",
	}
}

func terminalDevelopmentArguments() []string {
	return []string{
		"dev", "--target", "spice-agent",
		"--exclude=cmd/spice-agentd/**",
		"--exclude=internal/daemon/**",
		"--exclude=internal/daemoncommand/**",
		"--exclude=internal/spicegen/spice_agentd/**",
		"--exclude=.spice/spice_agentd.manifest.json",
		"--exclude=internal/architectureproof/**",
		"./cmd/spice-agent",
	}
}

func productionDevelopmentEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, item := range developmentEnvironment("") {
		name, value, found := strings.Cut(item, "=")
		if found {
			values[strings.ToUpper(name)] = value
		}
	}
	values["GOFLAGS"] = "-mod=vendor -tags=spice_acceptance"
	for name, value := range overrides {
		values[strings.ToUpper(name)] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

func startProductionSupervisor(
	t *testing.T,
	spiceExecutable, workspace string,
	arguments, environment []string,
	output *eventBuffer,
) *productionSupervisor {
	t.Helper()
	command := exec.Command(spiceExecutable, arguments...) // #nosec G204 -- exact test-built vendored CLI and fixed production arguments.
	command.Dir = workspace
	command.Env = environment
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("open Spice development input: %v", err)
	}
	command.Stdout = output
	command.Stderr = output
	configureDevelopmentCommand(command)
	if err = command.Start(); err != nil {
		t.Fatalf("start Spice development supervisor: %v", errors.Join(err, input.Close()))
	}
	file, ok := input.(*os.File)
	if !ok {
		t.Fatalf("Spice development input has unexpected type %T", input)
	}
	return &productionSupervisor{
		developmentProcess: newDevelopmentProcess(command, output, ""),
		input:              file,
	}
}

func (supervisor *productionSupervisor) write(t *testing.T, value string) {
	t.Helper()
	if _, err := supervisor.input.WriteString(value); err != nil {
		t.Fatalf("write development terminal input: %v\n%s", err, diagnosticTail(supervisor.output.String()))
	}
}

func runProductionPrompt(
	t *testing.T,
	terminal *productionSupervisor,
	output *eventBuffer,
	providerDirectory, prompt string,
	completed int,
) {
	t.Helper()
	before := output.Count("dual-supervisor-two")
	beginProductionPrompt(t, terminal, output, prompt)
	awaitProductionPrompt(t, output, providerDirectory, completed, before)
}

func beginProductionPrompt(
	t *testing.T,
	terminal *productionSupervisor,
	output *eventBuffer,
	prompt string,
) {
	t.Helper()
	terminal.write(t, prompt)
	output.waitForText(t, prompt)
	terminal.write(t, "\r")
}

func awaitProductionPrompt(
	t *testing.T,
	output *eventBuffer,
	providerDirectory string,
	completed, before int,
) {
	t.Helper()
	waitForDevelopmentFile(t, filepath.Join(providerDirectory, "checkpoint"), output)
	if err := os.WriteFile(filepath.Join(providerDirectory, "release"), []byte("release\n"), 0o600); err != nil {
		t.Fatalf("release deterministic development provider: %v", err)
	}
	output.waitForCount(t, "dual-supervisor-two", before+1)
	output.waitForText(t, fmt.Sprintf("Completed runs: %d", completed))
}

func removeProviderHandshake(t *testing.T, directory string) {
	t.Helper()
	for _, name := range []string{"checkpoint", "release"} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remove provider %s: %v", name, err)
		}
	}
}

func waitForDevelopmentFile(t *testing.T, path string, output *eventBuffer) {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s\n%s", path, diagnosticTail(output.String()))
		}
	}
}

func waitForDevelopmentEndpoint(
	t *testing.T,
	store *endpoint.Store,
	previous *endpoint.Metadata,
	output *eventBuffer,
) endpoint.Metadata {
	t.Helper()
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		metadata, err := store.Discover(t.Context())
		if err == nil && (previous == nil ||
			!bytes.Equal(metadata.Process().InstanceID(), previous.Process().InstanceID())) {
			return metadata
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for development daemon endpoint\n%s", diagnosticTail(output.String()))
		}
	}
}

func assertDevelopmentEndpointIdentity(
	t *testing.T,
	store *endpoint.Store,
	want endpoint.Metadata,
	output *eventBuffer,
) {
	t.Helper()
	got, err := store.Discover(t.Context())
	if err != nil {
		t.Fatalf("discover last-known-good endpoint: %v\n%s", err, diagnosticTail(output.String()))
	}
	if got.Process().ID() != want.Process().ID() ||
		!bytes.Equal(got.Process().InstanceID(), want.Process().InstanceID()) {
		t.Fatalf("last-known-good daemon identity changed from %d/%x to %d/%x",
			want.Process().ID(), want.Process().InstanceID(), got.Process().ID(), got.Process().InstanceID())
	}
}

func waitForDevelopmentEndpointAbsent(t *testing.T, store *endpoint.Store, output *eventBuffer) {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := store.Discover(t.Context())
		if errors.Is(err, endpoint.ErrNotFound) {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("development daemon endpoint remained published: %v\n%s", err, diagnosticTail(output.String()))
		}
	}
}

func assertProductionTerminalUnchanged(
	t *testing.T,
	supervisor *productionSupervisor,
	supervisorPID int,
	identity developmentApplicationIdentity,
	output *eventBuffer,
) {
	t.Helper()
	if supervisor.command.Process.Pid != supervisorPID || supervisor.command.ProcessState != nil {
		t.Fatalf("terminal supervisor identity/liveness changed: pid=%d state=%v, want pid=%d running",
			supervisor.command.Process.Pid, supervisor.command.ProcessState, supervisorPID)
	}
	got := waitForDevelopmentApplicationIdentity(t, supervisorPID, output)
	if got != identity {
		t.Fatalf("terminal candidate identity changed: got %#v, want %#v\n%s",
			got, identity, diagnosticTail(output.String()))
	}
	if count := output.Count("spice dev: application started"); count != 1 {
		t.Fatalf("terminal application start count = %d, want 1\n%s", count, diagnosticTail(output.String()))
	}
}

func assertProductionHistory(
	t *testing.T,
	terminal *productionSupervisor,
	output *eventBuffer,
) {
	t.Helper()
	secondCount := output.Count("second supervisor prompt")
	terminal.write(t, "\x1b[A")
	output.waitForCount(t, "second supervisor prompt", secondCount+1)
	firstCount := output.Count("first supervisor prompt")
	terminal.write(t, "\x1b[A")
	output.waitForCount(t, "first supervisor prompt", firstCount+1)
	plain := stripDevelopmentTerminalControl(output.String())
	if !strings.Contains(plain, "Completed runs: 2") {
		t.Fatalf("terminal history lost completed runs\n%s", diagnosticTail(plain))
	}
}

func waitForOutputCount(t *testing.T, output *eventBuffer, text string, count int) {
	t.Helper()
	output.waitForCount(t, text, count)
}

func stripDevelopmentTerminalControl(value string) string {
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

func productionDevelopmentHashes(t *testing.T, workspace string) map[string]string {
	t.Helper()
	result := prefixedTreeHashes(t, filepath.Join(workspace, ".spice"), ".spice")
	for _, directory := range []string{"cmd", "internal"} {
		maps.Copy(result, prefixedTreeHashes(t, filepath.Join(workspace, directory), directory))
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		digest := sha256Bytes(readFile(t, filepath.Join(workspace, name)))
		result[name] = digest
	}
	return result
}

func sha256Bytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
