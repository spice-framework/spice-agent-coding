package architecturee2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
	openaiprovider "github.com/spice-framework/spice-agent-provider-openai"
	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/client/localclient"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice-agent/daemon/localipc"
	"github.com/spice-framework/spice-agent/model"
	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
	spicebean "github.com/spice-framework/spice/bean"
	spiceconfig "github.com/spice-framework/spice/config"
	spicelifecycle "github.com/spice-framework/spice/lifecycle"
	"google.golang.org/grpc"
)

const (
	fixtureManifest = "spice-agent-distribution-fixture"
	fixtureVersion  = "v1"
	fixtureTool     = "fixture.echo"
	providerSecret  = "decisive-architecture-proof-secret"
	workspaceMarker = "Spice Agent decisive architecture proof"
)

var endpointSequence atomic.Uint64

func TestGeneratedDistributionExecutesCompiledAndRuntimeToolsAcrossReconnect(t *testing.T) {
	root := repositoryRoot(t)
	executable, digest := buildFixture(t, root)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(workspaceMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	responses := newResponsesFixture(t, true, true)
	provider := newProofProvider(t, responses)

	harness := newHarness(t, workspace, executable, digest, provider)
	defer func() {
		if closeErr := harness.close(); closeErr != nil {
			t.Errorf("close architecture proof: %v", closeErr)
		}
	}()
	generatedLimits := harness.application.Components().ServerLimits
	if generatedLimits.ReplayEvents() != 4096 || generatedLimits.ReplayBytes() != 8<<20 {
		t.Fatalf("generated replay limits = %d events/%d bytes", generatedLimits.ReplayEvents(), generatedLimits.ReplayBytes())
	}

	firstSession := harness.initialize(t, nil)
	firstConnection := firstSession.Connection()
	definition, ok := firstConnection.Catalog().Find(mustDefinitionRef(t, "coding", "v1"))
	if !ok || definition.Model() != "architecture-e2e-model" {
		t.Fatalf("generated catalog definition = %#v, found = %v", definition, ok)
	}
	started := startRun(t, firstSession, definition.Ref())
	if started.InitialSequence() != 1 || started.DuplicateOperation() || started.PlanID() == "" ||
		!strings.HasPrefix(started.Run().ID(), "run-") {
		t.Fatalf("start result = run %q sequence %d plan %q duplicate %v", started.Run().ID(), started.InitialSequence(), started.PlanID(), started.DuplicateOperation())
	}

	responses.waitForFirstRequest(t)
	firstEvents, acknowledged := readThroughTool(
		t, firstSession, started.Run(), "read", responses.releaseFirstResponse,
	)
	responses.waitForFinalRequest(t)

	claim, err := client.NewReconnectClaim(firstConnection.ClientID(), firstConnection.OwnershipEpoch())
	if err != nil {
		t.Fatal(err)
	}
	reconnected := harness.initialize(t, &claim)
	if reconnected.Connection().ClientID() != firstConnection.ClientID() ||
		reconnected.Connection().OwnershipEpoch() != firstConnection.OwnershipEpoch()+1 {
		t.Fatalf("reconnected ownership = %q/%d, want %q/%d", reconnected.Connection().ClientID(), reconnected.Connection().OwnershipEpoch(), firstConnection.ClientID(), firstConnection.OwnershipEpoch()+1)
	}
	if _, err = firstSession.Health(t.Context()); !errors.Is(err, client.ErrClosed) {
		t.Fatalf("fenced session health error = %v, want client.ErrClosed", err)
	}
	responses.releaseFinalResponse()
	remainingEvents, replay := readToRunTerminal(t, reconnected, started.Run(), acknowledged, 3)
	allEvents := append(slices.Clone(firstEvents), remainingEvents...)

	assertExecution(t, started, allEvents)
	if replay.pages < 2 || !replay.hasMore {
		t.Fatalf("bounded replay = pages %d, has-more %v; want multiple pages", replay.pages, replay.hasMore)
	}
	responses.assert(t)
	assertSecretsAbsent(t, harness, responses, allEvents)

	if err = reconnected.Close(); err != nil {
		t.Fatal(err)
	}
	if err = firstSession.Close(); err != nil {
		t.Fatal(err)
	}
	if err = harness.close(); err != nil {
		t.Fatal(err)
	}
	assertCleanup(t, harness)
}

func TestGeneratedCompletedRunRemainsReplayableWithProductionLimits(t *testing.T) {
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
			t.Errorf("close completed-run proof: %v", closeErr)
		}
	}()
	session := harness.initialize(t, nil)
	definition := mustDefinitionRef(t, "coding", "v1")
	started := startRun(t, session, definition)
	select {
	case <-responses.finalServed:
	case <-time.After(10 * time.Second):
		t.Fatal("immediate provider did not reach its final response")
	}
	events, _ := readToRunTerminal(
		t,
		session,
		started.Run(),
		0,
		session.Connection().Limits().ReplayEvents(),
	)
	assertExecution(t, started, events)
	responses.assert(t)
	assertSecretsAbsent(t, harness, responses, events)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := harness.close(); err != nil {
		t.Fatal(err)
	}
	assertCleanup(t, harness)
}

func newProofProvider(t *testing.T, responses *responsesFixture) model.Provider {
	t.Helper()
	provider, err := openaiprovider.New(
		openaiprovider.Config{
			APIKey: providerSecret, BaseURL: responses.server.URL + "/v1",
			Timeout: 5 * time.Second, MaxRetries: 0,
		},
		openaiprovider.WithHTTPClient(responses.server.Client()),
	)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

type proofHarness struct {
	application *spicegen.Application
	connector   *localclient.Connector
	listener    net.Listener
	serveDone   chan error
	journal     *lifecycleJournal
	closeOnce   sync.Once
	closeErr    error
}

func newHarness(
	t *testing.T,
	workspace, executable, digest string,
	provider model.Provider,
) *proofHarness {
	t.Helper()
	values, err := spiceconfig.NewMapSource("architecture-e2e", map[string]string{
		"agent.openai.api-key":                      providerSecret,
		"agent.model":                               "architecture-e2e-model",
		"agent.workspace":                           workspace,
		"agent.runtime-plugin.required":             "true",
		"agent.runtime-plugin.id":                   "architecture-e2e-fixture",
		"agent.runtime-plugin.path":                 executable,
		"agent.runtime-plugin.sha256":               digest,
		"agent.runtime-plugin.manifest-name":        fixtureManifest,
		"agent.runtime-plugin.manifest-version":     fixtureVersion,
		"agent.runtime-plugin.timeouts.startup":     "5s",
		"agent.runtime-plugin.timeouts.call":        "5s",
		"agent.runtime-plugin.timeouts.drain":       "5s",
		"agent.runtime-plugin.timeouts.shutdown":    "5s",
		"agent.runtime-plugin.timeouts.containment": "5s",
	})
	if err != nil {
		t.Fatal(err)
	}
	journal := &lifecycleJournal{}
	application, err := spicegen.NewApplicationWithOptions(t.Context(), spicegen.ApplicationOptions{
		Sources: []spiceconfig.Source{values},
		Overrides: spicegen.BeanOverrides{
			OpenAIModelProvider: spicebean.Replace[model.Provider](provider),
		},
		Observers: []spicelifecycle.Observer{journal.observe},
	})
	if err != nil {
		t.Fatalf("construct generated daemon: %v", err)
	}
	components := application.Components()
	activationContext, cancelActivation := context.WithTimeout(t.Context(), 10*time.Second)
	err = components.RuntimePluginActivation.Start(activationContext)
	cancelActivation()
	if err != nil {
		if cleanupErr := abortConstruction(application, nil); cleanupErr != nil {
			t.Errorf("clean up failed activation: %v", cleanupErr)
		}
		t.Fatalf("activate generated runtime plugin: %v", err)
	}
	if err = components.RuntimePluginActivation.PublicationReady(); err != nil {
		if cleanupErr := abortConstruction(application, nil); cleanupErr != nil {
			t.Errorf("clean up failed publication gate: %v", cleanupErr)
		}
		t.Fatalf("runtime plugin publication gate: %v", err)
	}

	transport, address := localEndpoint(t)
	listener, err := localipc.Listen(address)
	if err != nil {
		if cleanupErr := abortConstruction(application, nil); cleanupErr != nil {
			t.Errorf("clean up failed listener: %v", cleanupErr)
		}
		t.Fatalf("listen on private architecture-proof endpoint: %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- components.GrpcServer.Serve(listener) }()
	process, err := endpoint.GenerateProcess(testProcessID(t), time.Now().UTC())
	if err != nil {
		if cleanupErr := abortConstruction(application, listener); cleanupErr != nil {
			t.Errorf("clean up failed process metadata: %v", cleanupErr)
		}
		t.Fatal(err)
	}
	metadata, err := endpoint.NewMetadata(
		transport, address, components.EndpointToken, components.ServerBuild,
		components.ServerProtocol, process,
	)
	if err != nil {
		if cleanupErr := abortConstruction(application, listener); cleanupErr != nil {
			t.Errorf("clean up failed endpoint metadata: %v", cleanupErr)
		}
		t.Fatal(err)
	}
	connector, err := localclient.New(metadata)
	if err != nil {
		if cleanupErr := abortConstruction(application, listener); cleanupErr != nil {
			t.Errorf("clean up failed connector: %v", cleanupErr)
		}
		t.Fatal(err)
	}
	return &proofHarness{
		application: application, connector: connector, listener: listener,
		serveDone: serveDone, journal: journal,
	}
}

func abortConstruction(application *spicegen.Application, listener net.Listener) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var result error
	if listener != nil {
		result = errors.Join(result, application.Components().GrpcServer.Shutdown(ctx))
		result = errors.Join(result, ignoreClosed(listener.Close()))
	}
	return errors.Join(result, application.Stop(ctx))
}

func (harness *proofHarness) initialize(t *testing.T, claim *client.ReconnectClaim) client.Session {
	t.Helper()
	components := harness.application.Components()
	build, err := client.NewBuild("architecture-e2e-client", "v1", "fixture", runtime.Version())
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := client.NewProtocolRange(components.ServerProtocol, components.ServerProtocol)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := client.NewInitializationAttemptID()
	if err != nil {
		t.Fatal(err)
	}
	capabilities := []string{"events"}
	var request client.InitializeRequest
	if claim == nil {
		request, err = client.NewInitializeRequestWithAttempt(
			protocol, build, capabilities, capabilities, components.ServerLimits, attempt,
		)
	} else {
		request, err = client.NewReconnectRequestWithAttempt(
			protocol, build, capabilities, capabilities, components.ServerLimits, *claim, attempt,
		)
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	session, err := harness.connector.Initialize(ctx, request)
	if err != nil {
		t.Fatalf("initialize authenticated local client: %v", err)
	}
	return session
}

func (harness *proofHarness) close() error {
	if harness == nil {
		return nil
	}
	harness.closeOnce.Do(func() {
		var result error
		if harness.connector != nil {
			result = errors.Join(result, harness.connector.Close())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if harness.application != nil {
			result = errors.Join(result, harness.application.Components().GrpcServer.Shutdown(ctx))
		}
		if harness.listener != nil {
			result = errors.Join(result, ignoreClosed(harness.listener.Close()))
		}
		if harness.serveDone != nil {
			select {
			case err := <-harness.serveDone:
				if err != nil && !errors.Is(err, grpc.ErrServerStopped) && !errors.Is(err, net.ErrClosed) {
					result = errors.Join(result, err)
				}
			case <-ctx.Done():
				result = errors.Join(result, context.Cause(ctx))
			}
		}
		if harness.application != nil {
			result = errors.Join(result, harness.application.Stop(ctx))
		}
		harness.closeErr = result
	})
	return harness.closeErr
}

func ignoreClosed(err error) error {
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func startRun(t *testing.T, session client.Session, definition client.DefinitionRef) client.StartResult {
	t.Helper()
	operation, err := client.NewOperationID("architecture-e2e-start")
	if err != nil {
		t.Fatal(err)
	}
	input, err := client.NewInput("architecture-e2e-message", "prove the generated distribution")
	if err != nil {
		t.Fatal(err)
	}
	request, err := client.NewStartRequest(operation, definition, input)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result, err := session.Start(ctx, request)
	if err != nil {
		t.Fatalf("start architecture proof run: %v", err)
	}
	return result
}

func readThroughTool(
	t *testing.T,
	session client.Session,
	run client.RunRef,
	toolName string,
	releaseProvider func(),
) ([]client.Event, uint64) {
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
			t.Errorf("close initial event stream: %v", closeErr)
		}
	}()
	releaseProvider()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var events []client.Event
	for {
		frame, nextErr := stream.Next(ctx)
		if nextErr != nil {
			t.Fatalf("read initial event stream after %v: %v", summarizeEvents(events), nextErr)
		}
		current, ok := frame.Event()
		if !ok {
			continue
		}
		events = append(events, current)
		terminal, terminalOK := current.Detail().ToolTerminal()
		if current.Kind() == client.EventToolCompleted && terminalOK && terminal.Name() == toolName {
			return events, current.Sequence()
		}
	}
}

func summarizeEvents(events []client.Event) []string {
	result := make([]string, 0, len(events))
	for _, current := range events {
		value := fmt.Sprintf("%d:%s", current.Sequence(), current.Kind())
		if status, ok := current.Detail().Status(); ok {
			value += ":" + status
		}
		result = append(result, value)
	}
	return result
}

type replayEvidence struct {
	pages   int
	hasMore bool
}

func readToRunTerminal(
	t *testing.T,
	session client.Session,
	run client.RunRef,
	after uint64,
	limit uint32,
) ([]client.Event, replayEvidence) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var events []client.Event
	evidence := replayEvidence{}
	for {
		cursor, err := client.NewCursor(run, after)
		if err != nil {
			t.Fatal(err)
		}
		options, err := client.NewEventStreamOptions(limit, true, session.Connection().Limits())
		if err != nil {
			t.Fatal(err)
		}
		stream, err := session.Events(ctx, cursor, options)
		if err != nil {
			t.Fatal(err)
		}
		evidence.pages++
		for {
			frame, nextErr := stream.Next(ctx)
			if nextErr != nil {
				closeErr := stream.Close()
				t.Fatalf("read reconnected event stream: %v", errors.Join(nextErr, closeErr))
			}
			if current, ok := frame.Event(); ok {
				events = append(events, current)
				after = current.Sequence()
				if current.Kind() == client.EventRunCompleted {
					if closeErr := stream.Close(); closeErr != nil {
						t.Fatal(closeErr)
					}
					return events, evidence
				}
				continue
			}
			control, ok := frame.Control()
			if !ok || !control.HasMore() {
				continue
			}
			evidence.hasMore = true
			break
		}
		if closeErr := stream.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}

func assertExecution(t *testing.T, started client.StartResult, events []client.Event) {
	t.Helper()
	var toolStarts, toolTerminals []string
	var progress, finalText string
	runTerminals := 0
	for index, current := range events {
		wantSequence := uint64(index + 1)
		if current.Sequence() != wantSequence || current.Run().ID() != started.Run().ID() {
			t.Fatalf("event %d = run %q sequence %d, want %q/%d", index, current.Run().ID(), current.Sequence(), started.Run().ID(), wantSequence)
		}
		switch current.Kind() {
		case client.EventRunCompleted, client.EventRunFailed, client.EventRunCancelled:
			runTerminals++
		}
		if _, name, ok := current.Detail().ToolStart(); ok {
			toolStarts = append(toolStarts, name)
		}
		if terminal, ok := current.Detail().ToolTerminal(); ok {
			toolTerminals = append(toolTerminals, terminal.Name())
		}
		if _, message, ok := current.Detail().ToolProgress(); ok && message == "echo accepted" {
			progress = message
		}
		if text, ok := current.Detail().Text(); ok {
			finalText += text
		}
	}
	if runTerminals != 1 || events[len(events)-1].Kind() != client.EventRunCompleted {
		t.Fatalf("run terminal events = %d, final kind = %s", runTerminals, events[len(events)-1].Kind())
	}
	if !slices.Equal(toolStarts, []string{"read", fixtureTool}) ||
		!slices.Equal(toolTerminals, []string{"read", fixtureTool}) {
		t.Fatalf("tool lifecycle = starts %v terminals %v", toolStarts, toolTerminals)
	}
	if progress != "echo accepted" || finalText != "architecture proof complete" {
		t.Fatalf("portable output = progress %q, final text %q", progress, finalText)
	}
	definition, ok := events[0].Detail().Definition()
	if events[0].Kind() != client.EventRunStarted || !ok || definition != "coding" {
		t.Fatalf("first event = %s %#v", events[0].Kind(), events[0].Detail())
	}
}

func assertSecretsAbsent(
	t *testing.T,
	harness *proofHarness,
	responses *responsesFixture,
	events []client.Event,
) {
	t.Helper()
	authorization, err := harness.application.Components().EndpointToken.AuthorizationValue()
	if err != nil {
		t.Fatal(err)
	}
	visible := fmt.Sprint(harness.connector)
	if strings.Contains(visible, authorization) || !strings.Contains(visible, "[REDACTED]") {
		t.Fatalf("connector formatting is not redacted: %q", visible)
	}
	for _, body := range responses.bodiesSnapshot() {
		if strings.Contains(body, providerSecret) {
			t.Fatal("provider credential entered a Responses request body")
		}
	}
	for _, current := range events {
		for _, value := range eventStrings(current) {
			if strings.Contains(value, providerSecret) || strings.Contains(value, authorization) {
				t.Fatalf("event %d %s exposed a credential", current.Sequence(), current.Kind())
			}
		}
	}
}

func eventStrings(current client.Event) []string {
	detail := current.Detail()
	values := []string{string(current.Kind())}
	if text, ok := detail.Text(); ok {
		values = append(values, text)
	}
	if failure, ok := detail.ModelFailure(); ok {
		values = append(values, failure.Code(), failure.Message())
	}
	if _, name, ok := detail.ToolStart(); ok {
		values = append(values, name)
	}
	if _, message, ok := detail.ToolProgress(); ok {
		values = append(values, message)
	}
	if terminal, ok := detail.ToolTerminal(); ok {
		values = append(values, terminal.Name(), terminal.Problem())
	}
	if status, ok := detail.Status(); ok {
		values = append(values, status)
	}
	return values
}

func assertCleanup(t *testing.T, harness *proofHarness) {
	t.Helper()
	health := harness.application.Components().RuntimePluginHost.Health()
	if err := health.Validate(); err != nil || health.State() != pluginhost.HealthStateStopped ||
		health.ActiveLeases() != 0 || health.RetainedGenerations() != 0 {
		t.Fatalf("runtime plugin cleanup health = %s, validation = %v", health, err)
	}
	components := harness.journal.cleanupComponents()
	runHost := containingIndex(components, "NewRunHost")
	pluginHost := containingIndex(components, "NewRuntimePluginHost")
	rootRegistry := containingIndex(components, "NewRootRegistry")
	if runHost < 0 || pluginHost <= runHost || rootRegistry <= pluginHost {
		t.Fatalf("generated cleanup order run-host=%d plugin-host=%d root-registry=%d: %v", runHost, pluginHost, rootRegistry, components)
	}
}

type lifecycleJournal struct {
	mu         sync.Mutex
	components []string
}

func (journal *lifecycleJournal) observe(_ context.Context, observation spicelifecycle.Observation) {
	if observation.Operation != spicelifecycle.OperationCleanup || observation.Phase != spicelifecycle.PhaseEnd {
		return
	}
	journal.mu.Lock()
	journal.components = append(journal.components, observation.Component)
	journal.mu.Unlock()
}

func (journal *lifecycleJournal) cleanupComponents() []string {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return slices.Clone(journal.components)
}

func containingIndex(values []string, contains string) int {
	for index, value := range values {
		if strings.Contains(value, contains) {
			return index
		}
	}
	return -1
}

type responsesFixture struct {
	server           *httptest.Server
	holdFirst        bool
	holdFinal        bool
	firstSeen        chan struct{}
	allowFirst       chan struct{}
	finalSeen        chan struct{}
	allowFinal       chan struct{}
	firstOnce        sync.Once
	releaseOnce      sync.Once
	finalSeenOnce    sync.Once
	finalReleaseOnce sync.Once
	finalServed      chan struct{}
	finalOnce        sync.Once
	mu               sync.Mutex
	requests         int
	authorized       bool
	bodies           []string
	violation        string
}

func newResponsesFixture(t *testing.T, holdFirst, holdFinal bool) *responsesFixture {
	t.Helper()
	fixture := &responsesFixture{
		holdFirst: holdFirst, holdFinal: holdFinal,
		firstSeen: make(chan struct{}), allowFirst: make(chan struct{}),
		finalSeen: make(chan struct{}), allowFinal: make(chan struct{}),
		finalServed: make(chan struct{}), authorized: true,
	}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (fixture *responsesFixture) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" {
		fixture.fail(writer, "unexpected request target")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1<<20))
	if err != nil || !json.Valid(body) {
		fixture.fail(writer, "invalid request JSON")
		return
	}
	text := string(body)
	fixture.mu.Lock()
	fixture.requests++
	requestNumber := fixture.requests
	fixture.authorized = fixture.authorized && request.Header.Get("Authorization") == "Bearer "+providerSecret
	fixture.bodies = append(fixture.bodies, text)
	fixture.mu.Unlock()
	if strings.Contains(text, providerSecret) {
		fixture.fail(writer, "provider credential leaked into request body")
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	switch requestNumber {
	case 1:
		if !strings.Contains(text, `"name":"read"`) || !strings.Contains(text, `"name":"fixture.echo"`) {
			fixture.fail(writer, "generated compiled and runtime tools were not declared")
			return
		}
		fixture.firstOnce.Do(func() { close(fixture.firstSeen) })
		if fixture.holdFirst {
			select {
			case <-fixture.allowFirst:
			case <-request.Context().Done():
				return
			}
		}
		fixture.writeEvents(
			writer,
			`{"type":"response.completed","sequence_number":1,"response":{"id":"e2e-1","model":"architecture-e2e-model","status":"completed","service_tier":"default","usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6},"output":[{"type":"function_call","id":"item-read","call_id":"call-read","name":"read","arguments":"{\"path\":\"README.md\"}","status":"completed"}]}}`,
		)
	case 2:
		if !strings.Contains(text, `"type":"function_call_output"`) || !strings.Contains(text, workspaceMarker) {
			fixture.fail(writer, "compiled read result was not preserved in continuation")
			return
		}
		fixture.writeEvents(
			writer,
			`{"type":"response.completed","sequence_number":1,"response":{"id":"e2e-2","model":"architecture-e2e-model","status":"completed","service_tier":"default","usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10},"output":[{"type":"function_call","id":"item-plugin","call_id":"call-plugin","name":"fixture.echo","arguments":"{\"value\":\"runtime-plugin\"}","status":"completed"}]}}`,
		)
	case 3:
		if !strings.Contains(text, `"type":"function_call_output"`) || !strings.Contains(text, "runtime-plugin") {
			fixture.fail(writer, "runtime plugin result was not preserved in continuation")
			return
		}
		fixture.finalSeenOnce.Do(func() { close(fixture.finalSeen) })
		if fixture.holdFinal {
			select {
			case <-fixture.allowFinal:
			case <-request.Context().Done():
				return
			}
		}
		fixture.writeEvents(
			writer,
			`{"type":"response.output_text.delta","sequence_number":1,"item_id":"item-final","output_index":0,"content_index":0,"delta":"architecture proof complete"}`,
			`{"type":"response.completed","sequence_number":2,"response":{"id":"e2e-3","model":"architecture-e2e-model","status":"completed","service_tier":"default","usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15},"output":[{"type":"message","id":"item-final","role":"assistant","status":"completed","content":[{"type":"output_text","text":"architecture proof complete","annotations":[]}] }]}}`,
		)
		fixture.finalOnce.Do(func() { close(fixture.finalServed) })
	default:
		fixture.fail(writer, "unexpected extra provider request")
	}
}

func (fixture *responsesFixture) waitForFirstRequest(t *testing.T) {
	t.Helper()
	select {
	case <-fixture.firstSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not receive the first request")
	}
}

func (fixture *responsesFixture) releaseFirstResponse() {
	fixture.releaseOnce.Do(func() { close(fixture.allowFirst) })
}

func (fixture *responsesFixture) waitForFinalRequest(t *testing.T) {
	t.Helper()
	select {
	case <-fixture.finalSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not receive the final continuation request")
	}
}

func (fixture *responsesFixture) releaseFinalResponse() {
	fixture.finalReleaseOnce.Do(func() { close(fixture.allowFinal) })
}

func (fixture *responsesFixture) writeEvents(writer io.Writer, values ...string) {
	for _, value := range values {
		if _, err := fmt.Fprintf(writer, "data: %s\n\n", value); err != nil {
			fixture.recordViolation("write streaming response")
			return
		}
	}
	if _, err := io.WriteString(writer, "data: [DONE]\n\n"); err != nil {
		fixture.recordViolation("finish streaming response")
	}
}

func (fixture *responsesFixture) fail(writer http.ResponseWriter, message string) {
	fixture.recordViolation(message)
	http.Error(writer, message, http.StatusBadRequest)
}

func (fixture *responsesFixture) recordViolation(message string) {
	fixture.mu.Lock()
	if fixture.violation == "" {
		fixture.violation = message
	}
	fixture.mu.Unlock()
}

func (fixture *responsesFixture) assert(t *testing.T) {
	t.Helper()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.requests != 3 || !fixture.authorized || fixture.violation != "" {
		t.Fatalf("Responses fixture = requests %d authorized %v violation %q", fixture.requests, fixture.authorized, fixture.violation)
	}
}

func (fixture *responsesFixture) bodiesSnapshot() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return slices.Clone(fixture.bodies)
}

func buildFixture(t *testing.T, root string) (string, string) {
	t.Helper()
	name := "spice-agent-distribution-fixture"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext( // #nosec G204,G702 -- exact Go and fixed repository fixture.
		ctx, exactGoExecutable(), "build", "-mod=vendor", "-trimpath", "-buildvcs=false",
		"-ldflags=-buildid=", "-o", path, "./testdata/runtimeplugin/go",
	)
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("build offline fixture: stdout %q, stderr %q, error %v", stdout.String(), stderr.String(), err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("fixture build emitted output: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return path, hex.EncodeToString(sum[:])
}

func localEndpoint(t *testing.T) (endpoint.Transport, string) {
	t.Helper()
	sequence := endpointSequence.Add(1)
	if runtime.GOOS == "windows" {
		return endpoint.TransportWindowsNamedPipe,
			fmt.Sprintf(`\\.\pipe\spice-agent-e2e-%d-%d`, os.Getpid(), sequence)
	}
	directory, err := os.MkdirTemp("", "sae2e-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(directory, 0o700); err != nil {
		t.Fatal(errors.Join(err, os.RemoveAll(directory)))
	}
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(directory); removeErr != nil {
			t.Errorf("remove private endpoint directory: %v", removeErr)
		}
	})
	return endpoint.TransportUnixSocket, filepath.Join(directory, fmt.Sprintf("agent-%d.sock", sequence))
}

func testProcessID(t *testing.T) uint32 {
	t.Helper()
	processID := os.Getpid()
	if processID <= 0 || uint64(processID) > math.MaxUint32 {
		t.Fatal("test process ID is invalid")
	}
	return uint32(processID) // #nosec G115 -- bounded immediately above.
}

func mustDefinitionRef(t *testing.T, id, revision string) client.DefinitionRef {
	t.Helper()
	value, err := client.NewDefinitionRef(id, revision)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func exactGoExecutable() string {
	name := "go"
	if runtime.GOOS == "windows" {
		name = "go.exe"
	}
	return filepath.Join(runtime.GOROOT(), "bin", name) //nolint:staticcheck // Exact executing toolchain.
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	current, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		content, readErr := os.ReadFile(filepath.Join(current, "go.mod"))
		if readErr == nil && bytes.Contains(content, []byte("module github.com/spice-framework/spice-agent-coding\n")) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatal("locate spice-agent-coding module root")
		}
		current = parent
	}
}
