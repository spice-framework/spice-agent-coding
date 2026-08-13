//go:build spice_release_artifacts

package installedacceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/client/localclient"
)

const (
	decisiveWorkspaceInput  = "Spice Agent decisive exact-release input\n"
	decisiveWorkspaceOutput = "Spice Agent decisive exact-release output\n"
	decisiveFinalText       = "decisive exact-release workflow complete"
)

func verifyDecisiveReleaseWorkflow(t *testing.T) {
	set := verifiedReleaseSet(t)
	installRoot, err := set.ExtractNative(
		filepath.Join(t.TempDir(), "decisive π installed bytes"), runtime.GOOS, runtime.GOARCH,
	)
	if err != nil {
		t.Fatalf("extract decisive native release archive: %v", err)
	}
	daemonBinary := filepath.Join(installRoot, executableName("spice-agentd"))
	workspace := t.TempDir()
	if err = os.WriteFile(filepath.Join(workspace, "README.md"), []byte(decisiveWorkspaceInput), 0o600); err != nil {
		t.Fatal(err)
	}
	root := repositoryRoot(t)
	plugin := buildOfflineTestBinary(t, root, "spice-agent-distribution-fixture", "./testdata/runtimeplugin/go")
	provider := newDecisiveReleaseProvider(t)
	store, environment := releaseProcessEnvironment(t)
	environment["OPENAI_BASE_URL"] = provider.server.URL + "/v1"
	environment["OPENAI_MODEL"] = "decisive-release-model"
	environment["SPICE_AGENT_WORKSPACE"] = workspace
	configureAcceptancePlugin(environment, plugin, fileSHA256(t, plugin))
	environment["SPICE_AGENT_RUNTIME_PLUGIN_STARTUP_TIMEOUT"] = "30s"

	daemon := startProcess(t, daemonBinary, []string{"serve"}, environment)
	t.Cleanup(func() { daemon.stop(t, false) })
	metadata := waitForEndpoint(t, store, daemon, nil, "")
	assertReleasedServer(t, metadata, set)
	connector, err := localclient.New(metadata)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := connector.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})

	request := decisiveInitializeRequest(t, metadata.Protocol(), nil)
	session, err := connector.Initialize(t.Context(), request)
	if err != nil {
		t.Fatalf("initialize decisive release client: %v", err)
	}
	definition := session.Connection().Catalog().Definitions()[0]
	started := decisiveStart(t, session, definition.Ref(), "decisive workflow")
	events := decisiveReadToTerminal(t, session, started.Run(), nil)
	provider.assertWorkflow(t)
	assertDecisiveEvents(t, events, client.EventRunCompleted)
	shellHelper := buildOfflineTestBinary(t, root, "cancelhelper", "./testdata/cancelhelper")
	shellDirectory := t.TempDir()
	provider.configureShellCancellation(shellHelper, shellDirectory)
	content, err := os.ReadFile(filepath.Join(workspace, "phase6-output.txt"))
	if err != nil || string(content) != decisiveWorkspaceOutput {
		t.Fatalf("decisive replace output = %q, error %v", content, err)
	}

	claim, err := client.NewReconnectClaim(
		session.Connection().ClientID(), session.Connection().OwnershipEpoch(),
	)
	if err != nil {
		t.Fatal(err)
	}
	reconnected, err := connector.Initialize(
		t.Context(), decisiveInitializeRequest(t, metadata.Protocol(), &claim),
	)
	if err != nil {
		t.Fatalf("reconnect decisive release client: %v", err)
	}
	if _, healthErr := session.Health(t.Context()); !errors.Is(healthErr, client.ErrClosed) {
		t.Fatalf("prior release session health error = %v, want client.ErrClosed", healthErr)
	}

	events = cancelDecisiveRunWhenReady(
		t, reconnected, definition.Ref(), "cancel provider", provider.providerReady, nil,
	)
	assertDecisiveEvents(t, events, client.EventRunCancelled)
	shellReady := waitForDecisiveFile(t.Context(), filepath.Join(shellDirectory, "shell.ready"))
	var witnesses []*managedProcessWitness
	events = cancelDecisiveRunWhenReady(
		t, reconnected, definition.Ref(), "cancel shell", shellReady,
		func() error {
			var witnessErr error
			witnesses, witnessErr = openDecisiveShellWitnesses(shellDirectory)
			return witnessErr
		},
	)
	assertDecisiveEvents(t, events, client.EventRunCancelled)
	for _, witness := range witnesses {
		waitForScenarioProcessExit(t, witness, daemon)
		if closeErr := witness.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}
	cancelled := decisiveStart(t, reconnected, definition.Ref(), "cancel plugin")
	events = decisiveReadToTerminal(t, reconnected, cancelled.Run(), func() {
		cancelDecisiveRun(t, reconnected, cancelled.Run(), "plugin")
	})
	assertDecisiveEvents(t, events, client.EventRunCancelled)
	provider.assertAllCancellations(t)
	if err = reconnected.Close(); err != nil {
		t.Fatal(err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	if err = connector.Close(); err != nil {
		t.Fatal(err)
	}
	daemon.stop(t, true)
	assertEndpointAbsent(t, store, daemon)
	assertDecisiveSecretsAbsent(t, provider, daemon)
}

func cancelDecisiveRunWhenReady(
	t *testing.T,
	session client.Session,
	definition client.DefinitionRef,
	prompt string,
	ready <-chan struct{},
	beforeCancel func() error,
) []client.Event {
	t.Helper()
	started := decisiveStart(t, session, definition, prompt)
	done := make(chan error, 1)
	go func() {
		select {
		case <-ready:
		case <-t.Context().Done():
			done <- context.Cause(t.Context())
			return
		}
		if beforeCancel != nil {
			if err := beforeCancel(); err != nil {
				done <- err
				return
			}
		}
		operation, err := client.NewOperationID("decisive-release-" + strings.ReplaceAll(prompt, " ", "-"))
		if err == nil {
			var request client.CancelRequest
			request, err = client.NewCancelRequest(started.Run(), operation, "decisive proof")
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), observationTimeout)
				_, err = session.Cancel(ctx, request)
				cancel()
			}
		}
		done <- err
	}()
	events := decisiveReadToTerminal(t, session, started.Run(), nil)
	if err := <-done; err != nil {
		t.Fatalf("cancel decisive %s run: %v", prompt, err)
	}
	return events
}

func cancelDecisiveRun(
	t *testing.T,
	session client.Session,
	run client.RunRef,
	suffix string,
) {
	t.Helper()
	operation, err := client.NewOperationID("decisive-release-cancel-" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	request, err := client.NewCancelRequest(run, operation, "decisive proof")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Cancel(t.Context(), request); err != nil {
		t.Fatalf("cancel decisive release run: %v", err)
	}
}

func waitForDecisiveFile(ctx context.Context, path string) <-chan struct{} {
	ready := make(chan struct{})
	go func() {
		defer close(ready)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(path); err == nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return ready
}

func openDecisiveShellWitnesses(directory string) ([]*managedProcessWitness, error) {
	result := make([]*managedProcessWitness, 0, 2)
	for _, name := range []string{"root.pid", "child.pid"} {
		content, err := os.ReadFile(filepath.Join(directory, name)) // #nosec G304 -- exact test-owned path.
		if err != nil {
			return result, err
		}
		pid, err := strconv.ParseUint(strings.TrimSpace(string(content)), 10, 32)
		if err != nil || pid == 0 {
			return result, fmt.Errorf("decode decisive shell PID %q: %w", content, err)
		}
		witness, err := openManagedProcessWitness(uint32(pid))
		if err != nil {
			return result, err
		}
		result = append(result, witness)
	}
	return result, nil
}

func decisiveInitializeRequest(
	t *testing.T,
	protocol client.ProtocolVersion,
	claim *client.ReconnectClaim,
) client.InitializeRequest {
	t.Helper()
	protocolRange, err := client.NewProtocolRange(protocol, protocol)
	if err != nil {
		t.Fatal(err)
	}
	build, err := client.NewBuild("decisive-release-client", "v1", "test", runtime.Version())
	if err != nil {
		t.Fatal(err)
	}
	limits, err := client.NewLimits(4<<20, 512, 4096, 8<<20, 8, 64)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := client.NewInitializationAttemptID()
	if err != nil {
		t.Fatal(err)
	}
	capabilities := []string{"events", "snapshot-authority-v1", "snapshots"}
	if claim == nil {
		request, requestErr := client.NewInitializeRequestWithAttempt(
			protocolRange, build, capabilities, []string{"events"}, limits, attempt,
		)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		return request
	}
	request, err := client.NewReconnectRequestWithAttempt(
		protocolRange, build, capabilities, []string{"events"}, limits, *claim, attempt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func decisiveStart(
	t *testing.T,
	session client.Session,
	definition client.DefinitionRef,
	prompt string,
) client.StartResult {
	t.Helper()
	result, _ := decisiveStartAt(t, session, definition, prompt)
	return result
}

func decisiveStartAt(
	t *testing.T,
	session client.Session,
	definition client.DefinitionRef,
	prompt string,
) (client.StartResult, time.Time) {
	t.Helper()
	operation, err := client.NewOperationID(strings.ReplaceAll(prompt, " ", "-"))
	if err != nil {
		t.Fatal(err)
	}
	input, err := client.NewInput("decisive-release-message", prompt)
	if err != nil {
		t.Fatal(err)
	}
	request, err := client.NewStartRequest(operation, definition, input)
	if err != nil {
		t.Fatal(err)
	}
	invokedAt := time.Now()
	result, err := session.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start decisive release run: %v", err)
	}
	return result, invokedAt
}

func decisiveReadToTerminal(
	t *testing.T,
	session client.Session,
	run client.RunRef,
	onBlock func(),
) []client.Event {
	t.Helper()
	cursor, err := client.NewCursor(run, 0)
	if err != nil {
		t.Fatal(err)
	}
	options, err := client.NewEventStreamOptions(64, true, session.Connection().Limits())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := session.Events(t.Context(), cursor, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), observationTimeout)
	defer cancel()
	var events []client.Event
	blocked := false
	for {
		frame, nextErr := stream.Next(ctx)
		if nextErr != nil {
			t.Fatalf("read decisive release events after %v: %v", decisiveEventKinds(events), nextErr)
		}
		current, ok := frame.Event()
		if !ok {
			continue
		}
		events = append(events, current)
		if _, message, progress := current.Detail().ToolProgress(); onBlock != nil && !blocked && progress && message == "block ready" {
			blocked = true
			onBlock()
		}
		switch current.Kind() {
		case client.EventRunCompleted, client.EventRunCancelled, client.EventRunFailed:
			return events
		}
	}
}

func decisiveEventKinds(events []client.Event) []client.EventKind {
	result := make([]client.EventKind, 0, len(events))
	for _, current := range events {
		result = append(result, current.Kind())
	}
	return result
}

func assertDecisiveEvents(t *testing.T, events []client.Event, terminal client.EventKind) {
	t.Helper()
	if len(events) == 0 || events[len(events)-1].Kind() != terminal {
		failures := make([]string, 0)
		for _, current := range events {
			if failure, ok := current.Detail().ModelFailure(); ok {
				failures = append(failures, failure.Code()+": "+failure.Message())
			}
			if toolResult, ok := current.Detail().ToolTerminal(); ok && toolResult.Problem() != "" {
				failures = append(failures, toolResult.Name()+": "+toolResult.Problem())
			}
		}
		t.Fatalf("decisive event terminal = %v, want %s; failures: %v", decisiveEventKinds(events), terminal, failures)
	}
	terminalCount := 0
	var started, completed []string
	var text strings.Builder
	for index, current := range events {
		if current.Sequence() != uint64(index+1) {
			t.Fatalf("decisive event sequence %d = %d", index, current.Sequence())
		}
		switch current.Kind() {
		case client.EventRunCompleted, client.EventRunCancelled, client.EventRunFailed:
			terminalCount++
		}
		if _, name, ok := current.Detail().ToolStart(); ok {
			started = append(started, name)
		}
		if result, ok := current.Detail().ToolTerminal(); ok {
			completed = append(completed, result.Name())
		}
		if value, ok := current.Detail().Text(); ok {
			text.WriteString(value)
		}
	}
	if terminalCount != 1 {
		t.Fatalf("decisive terminal count = %d", terminalCount)
	}
	if terminal == client.EventRunCompleted {
		want := []string{"read", "replace", "shell", "fixture.echo"}
		if !slices.Equal(started, want) || !slices.Equal(completed, want) {
			t.Fatalf("decisive tools start=%v terminal=%v, want %v", started, completed, want)
		}
		if text.String() != decisiveFinalText {
			t.Fatalf("decisive final text = %q", text.String())
		}
	}
}

type decisiveReleaseProvider struct {
	server         *httptest.Server
	block          chan struct{}
	providerReady  chan struct{}
	providerOnce   sync.Once
	shellHelper    string
	shellDirectory string

	mu         sync.Mutex
	requests   int
	bodies     []string
	violation  string
	authorized bool
}

func newDecisiveReleaseProvider(t *testing.T) *decisiveReleaseProvider {
	t.Helper()
	provider := &decisiveReleaseProvider{
		block: make(chan struct{}), providerReady: make(chan struct{}), authorized: true,
	}
	provider.server = httptest.NewServer(http.HandlerFunc(provider.serveHTTP))
	t.Cleanup(provider.server.Close)
	return provider
}

func (provider *decisiveReleaseProvider) configureShellCancellation(helper, directory string) {
	provider.shellHelper = helper
	provider.shellDirectory = directory
}

func (provider *decisiveReleaseProvider) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1<<20))
	provider.mu.Lock()
	provider.requests++
	number := provider.requests
	provider.bodies = append(provider.bodies, string(body))
	provider.authorized = provider.authorized && request.Header.Get("Authorization") == "Bearer release-archive-check-only"
	provider.mu.Unlock()
	if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" || err != nil || !json.Valid(body) {
		provider.fail(writer, "invalid provider request")
		return
	}
	if strings.Contains(string(body), "release-archive-check-only") {
		provider.fail(writer, "provider credential leaked into body")
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	var response string
	if strings.Contains(string(body), "cancel provider") {
		provider.providerOnce.Do(func() { close(provider.providerReady) })
		<-request.Context().Done()
		return
	}
	if strings.Contains(string(body), "cancel shell") {
		arguments, marshalErr := json.Marshal(map[string]any{
			"argv": []string{provider.shellHelper, "root", provider.shellDirectory},
		})
		if marshalErr != nil {
			provider.fail(writer, "encode shell cancellation arguments")
			return
		}
		response = decisiveToolResponse("shell", string(arguments), "decisive-cancel-shell")
	} else if strings.Contains(string(body), "cancel plugin") {
		response = decisiveToolResponse("fixture.block", `{}`, "decisive-block")
		close(provider.block)
	} else {
		switch number {
		case 1:
			if !strings.Contains(string(body), `"name":"read"`) ||
				!strings.Contains(string(body), `"name":"replace"`) ||
				!strings.Contains(string(body), `"name":"shell"`) ||
				!strings.Contains(string(body), `"name":"fixture.echo"`) ||
				!strings.Contains(string(body), "unsandboxed") {
				provider.fail(writer, "compiled tools, plugin, or privilege warning was absent")
				return
			}
			response = decisiveToolResponse("read", `{"path":"README.md"}`, "decisive-read")
		case 2:
			if !strings.Contains(string(body), "README.md") {
				provider.fail(writer, "read result was absent from continuation")
				return
			}
			response = decisiveToolResponse(
				"replace", `{"path":"phase6-output.txt","content":"`+
					strings.TrimSuffix(decisiveWorkspaceOutput, "\n")+`\n","create":true}`, "decisive-replace",
			)
		case 3:
			if !strings.Contains(string(body), "phase6-output.txt") {
				provider.fail(writer, "replace result was absent from continuation")
				return
			}
			arguments := `{"argv":["sh","-c","printf decisive-shell"]}`
			if runtime.GOOS == "windows" {
				arguments = `{"argv":["cmd.exe","/d","/c","echo|set /p=decisive-shell"]}`
			}
			response = decisiveToolResponse("shell", arguments, "decisive-shell")
		case 4:
			if !strings.Contains(string(body), "decisive-shell") {
				provider.fail(writer, "shell result was absent from continuation")
				return
			}
			response = decisiveToolResponse("fixture.echo", `{"value":"decisive-plugin"}`, "decisive-plugin")
		case 5:
			if !strings.Contains(string(body), "decisive-plugin") {
				provider.fail(writer, "plugin result was absent from continuation")
				return
			}
			response = `{"type":"response.output_text.delta","sequence_number":1,"item_id":"decisive-final","output_index":0,"content_index":0,"delta":"` + decisiveFinalText + `"}` + "\n\n" +
				`data: {"type":"response.completed","sequence_number":2,"response":{"id":"decisive-final","model":"decisive-release-model","status":"completed","service_tier":"default","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"message","id":"decisive-final","role":"assistant","status":"completed","content":[{"type":"output_text","text":"` + decisiveFinalText + `","annotations":[]}]}]}}`
		default:
			provider.fail(writer, "unexpected provider request")
			return
		}
	}
	if _, err = fmt.Fprintf(writer, "data: %s\n\ndata: [DONE]\n\n", response); err != nil {
		provider.recordViolation("write provider response")
	}
}

func decisiveToolResponse(name, arguments, id string) string {
	return fmt.Sprintf(
		`{"type":"response.completed","sequence_number":1,"response":{"id":%q,"model":"decisive-release-model","status":"completed","service_tier":"default","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"function_call","id":%q,"call_id":%q,"name":%q,"arguments":%q,"status":"completed"}]}}`,
		id, "item-"+id, "call-"+id, name, arguments,
	)
}

func (provider *decisiveReleaseProvider) fail(writer http.ResponseWriter, message string) {
	provider.recordViolation(message)
	http.Error(writer, message, http.StatusBadRequest)
}

func (provider *decisiveReleaseProvider) recordViolation(message string) {
	provider.mu.Lock()
	if provider.violation == "" {
		provider.violation = message
	}
	provider.mu.Unlock()
}

func (provider *decisiveReleaseProvider) waitForBlock(ready chan<- struct{}) {
	<-provider.block
	close(ready)
}

func (provider *decisiveReleaseProvider) assertWorkflow(t *testing.T) {
	t.Helper()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.requests < 5 || !provider.authorized || provider.violation != "" {
		t.Fatalf("decisive provider requests=%d authorized=%v violation=%q", provider.requests, provider.authorized, provider.violation)
	}
}

func (provider *decisiveReleaseProvider) assertCancellation(t *testing.T) {
	t.Helper()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.requests != 6 || provider.violation != "" {
		t.Fatalf("decisive cancellation provider requests=%d violation=%q", provider.requests, provider.violation)
	}
}

func (provider *decisiveReleaseProvider) assertAllCancellations(t *testing.T) {
	t.Helper()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.requests != 8 || provider.violation != "" {
		t.Fatalf("decisive provider cancellations requests=%d violation=%q", provider.requests, provider.violation)
	}
}

func assertDecisiveSecretsAbsent(
	t *testing.T,
	provider *decisiveReleaseProvider,
	daemon *process,
) {
	t.Helper()
	provider.mu.Lock()
	bodies := slices.Clone(provider.bodies)
	provider.mu.Unlock()
	visible := append(bodies, daemon.stdout.String(), daemon.stderr.String())
	for _, value := range visible {
		if strings.Contains(value, "release-archive-check-only") {
			t.Fatal("decisive release evidence exposed its credential canary")
		}
	}
}
