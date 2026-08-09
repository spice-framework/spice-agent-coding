package architecturee2e_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	terminalgen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agent"
	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	spicebean "github.com/spice-framework/spice/bean"
	spiceconfig "github.com/spice-framework/spice/config"
	spicelifecycle "github.com/spice-framework/spice/lifecycle"
)

const terminalWorkflowPrompt = "prove the generated Bubble Tea workflow"

// TestGeneratedTerminalRendersCompleteArchitectureWorkflowThroughBubbleTea
// drives the generated terminal graph through its real Bubble Tea event loop.
// Its line-oriented accessible mode deliberately uses pipes, not a PTY or
// ConPTY; native terminal presentation remains a separate installed gate.
func TestGeneratedTerminalRendersCompleteArchitectureWorkflowThroughBubbleTea(t *testing.T) {
	root := repositoryRoot(t)
	executable, digest := buildFixture(t, root)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(workspaceMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	responses := newResponsesFixture(t, false, false)
	harness := newHarness(t, workspace, executable, digest, newProofProvider(t, responses))
	defer func() {
		if closeErr := harness.close(); closeErr != nil {
			t.Errorf("close Bubble Tea architecture harness: %v", closeErr)
		}
	}()

	input, inputWriter := io.Pipe()
	defer func() {
		if closeErr := inputWriter.Close(); closeErr != nil && !errors.Is(closeErr, io.ErrClosedPipe) {
			t.Errorf("close Bubble Tea input: %v", closeErr)
		}
	}()
	output := &terminalOutput{}
	streams, err := agenttui.NewTerminalIO(input, output)
	if err != nil {
		t.Fatal(err)
	}
	values, err := spiceconfig.NewMapSource("architecture-e2e-terminal", map[string]string{
		"agent.workspace":           workspace,
		"agent.terminal.mode":       "attach",
		"agent.terminal.endpoint":   "architecture-e2e-private-endpoint",
		"agent.terminal.accessible": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	terminalJournal := &lifecycleJournal{}
	storeOverride, capturedStore := privateTerminalStoreOverride(t)
	application, err := terminalgen.NewApplicationWithOptions(t.Context(), terminalgen.ApplicationOptions{
		Sources: []spiceconfig.Source{values},
		Overrides: terminalgen.BeanOverrides{
			OsTerminalIO:            spicebean.Replace(streams),
			TerminalEndpointStore:   storeOverride,
			TerminalClientConnector: spicebean.Replace[client.Connector](harness.connector),
		},
		Observers: []spicelifecycle.Observer{terminalJournal.observe},
	})
	if err != nil {
		t.Fatalf("construct generated Bubble Tea terminal: %v", err)
	}
	applicationStopped := false
	defer func() {
		if applicationStopped {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stopErr := application.Stop(ctx); stopErr != nil {
			t.Errorf("stop generated Bubble Tea terminal: %v", stopErr)
		}
	}()
	components := application.Components()
	if components.TerminalClientConnector != harness.connector || !components.TerminalConfig.Accessible() {
		t.Fatal("generated terminal did not select the proof connector and accessible Bubble Tea path")
	}
	startContext, cancelStart := context.WithTimeout(t.Context(), 10*time.Second)
	err = application.Start(startContext)
	cancelStart()
	if err != nil {
		t.Fatalf("start generated Bubble Tea terminal: %v", err)
	}

	shellContext, cancelShell := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancelShell()
	shellDone := make(chan error, 1)
	go func() { shellDone <- components.TerminalShell.Run(shellContext) }()
	waitForTerminalOutput(t, output, "[READY]")
	writeTerminalInput(t, inputWriter, terminalWorkflowPrompt+"\r")
	waitForTerminalOutput(t, output, "architecture proof complete")
	waitForTerminalOutput(t, output, "Completed runs: 1")
	responses.assert(t)
	assertSecretsAbsent(t, harness, responses, nil)
	writeTerminalInput(t, inputWriter, "\x1b[A")
	waitForTerminalOutput(t, output, "Prompt: "+terminalWorkflowPrompt)
	visible := output.String()
	for _, expected := range []string{"tool.completed: read", "tool.completed: fixture.echo"} {
		if !strings.Contains(visible, expected) {
			t.Fatalf("Bubble Tea output did not render %q:\n%s", expected, visible)
		}
	}
	if strings.Contains(visible, "Completed runs: 2") {
		t.Fatalf("Bubble Tea rendered more than one terminal run:\n%s", visible)
	}
	if strings.Contains(visible, providerSecret) {
		t.Fatal("Bubble Tea output exposed the provider credential")
	}

	writeTerminalInput(t, inputWriter, "\x11")
	select {
	case shellErr := <-shellDone:
		if shellErr != nil {
			t.Fatalf("run generated Bubble Tea terminal: %v", shellErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("generated Bubble Tea terminal did not stop after Ctrl+Q")
	}
	stopContext, cancelStop := context.WithTimeout(context.Background(), 10*time.Second)
	err = application.Stop(stopContext)
	cancelStop()
	if err != nil {
		t.Fatalf("stop generated Bubble Tea terminal: %v", err)
	}
	applicationStopped = true
	assertTerminalCleanup(t, terminalJournal, capturedStore)

	if err = harness.close(); err != nil {
		t.Fatalf("close Bubble Tea architecture harness: %v", err)
	}
	assertCleanup(t, harness)
}

func TestGeneratedTerminalRollsBackPrivateStateWhenSessionConstructionFails(t *testing.T) {
	streams, err := agenttui.NewTerminalIO(strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	values, err := spiceconfig.NewMapSource("architecture-e2e-terminal-failure", map[string]string{
		"agent.workspace":           t.TempDir(),
		"agent.terminal.mode":       "attach",
		"agent.terminal.endpoint":   "architecture-e2e-private-endpoint",
		"agent.terminal.accessible": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	journal := &lifecycleJournal{}
	storeOverride, capturedStore := privateTerminalStoreOverride(t)
	application, err := terminalgen.NewApplicationWithOptions(t.Context(), terminalgen.ApplicationOptions{
		Sources: []spiceconfig.Source{values},
		Overrides: terminalgen.BeanOverrides{
			OsTerminalIO:            spicebean.Replace(streams),
			TerminalEndpointStore:   storeOverride,
			TerminalClientConnector: spicebean.Replace[client.Connector](nil),
		},
		Observers: []spicelifecycle.Observer{journal.observe},
	})
	if application != nil || err == nil || !strings.Contains(err.Error(), "construct bean terminalSession") ||
		!strings.Contains(err.Error(), "connector must not be nil") {
		t.Fatalf("construct terminal with nil connector = application %#v, error %v", application, err)
	}
	assertTerminalCleanup(t, journal, capturedStore)
}

type terminalOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (output *terminalOutput) Write(value []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.Write(value)
}

func (output *terminalOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.String()
}

func writeTerminalInput(t *testing.T, writer *io.PipeWriter, value string) {
	t.Helper()
	if _, err := io.WriteString(writer, value); err != nil {
		t.Fatalf("write Bubble Tea input: %v", err)
	}
}

func waitForTerminalOutput(t *testing.T, output *terminalOutput, expected string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), expected) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Bubble Tea output did not contain %q:\n%s", expected, output.String())
}

func privateTerminalStoreOverride(
	t *testing.T,
) (spicebean.Override[*endpoint.Store], *terminalStoreCapture) {
	t.Helper()
	scope, err := endpoint.CurrentUserScope()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(
		scope.Directory(),
		fmt.Sprintf("architecture-e2e-terminal-%d-%d", os.Getpid(), endpointSequence.Add(1)),
	)
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(directory); removeErr != nil {
			t.Errorf("remove private terminal endpoint state: %v", removeErr)
		}
	})
	captured := &terminalStoreCapture{}
	override := spicebean.ReplaceFactory(func(context.Context) (*endpoint.Store, spicelifecycle.Cleanup, error) {
		store, openErr := endpoint.OpenStore(endpoint.StoreConfig{
			Directory: directory, PollInterval: 10 * time.Millisecond,
		})
		if openErr != nil {
			return nil, nil, openErr
		}
		captured.store = store
		return store, func(context.Context) error { return store.Close() }, nil
	})
	return override, captured
}

type terminalStoreCapture struct {
	store *endpoint.Store
}

func assertTerminalCleanup(t *testing.T, journal *lifecycleJournal, captured *terminalStoreCapture) {
	t.Helper()
	if captured == nil || captured.store == nil {
		t.Fatal("generated terminal did not construct its private endpoint store")
	}
	if _, err := captured.store.Discover(t.Context()); !errors.Is(err, endpoint.ErrClosed) {
		t.Fatalf("private terminal store after cleanup = %v, want endpoint.ErrClosed", err)
	}
	components := journal.cleanupComponents()
	session := containingIndex(components, "NewSession")
	endpointStore := containingIndex(components, "NewEndpointStore")
	if endpointStore < 0 || (session >= 0 && session >= endpointStore) {
		t.Fatalf("generated terminal cleanup order session=%d endpoint-store=%d: %v", session, endpointStore, components)
	}
}
