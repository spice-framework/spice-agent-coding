package architectureproof

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/event"
	"github.com/spice-framework/spice-agent/message"
	"github.com/spice-framework/spice-agent/model"
	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
)

func TestProofConstructionAndRunRejectInvalidState(t *testing.T) {
	t.Parallel()
	if dispatcher, err := NewToolDispatcher(map[string]tool.Tool{"broken": nil}); dispatcher != nil || err == nil || !strings.Contains(err.Error(), "dispatcher") {
		t.Fatalf("invalid tool dispatcher = %#v, %v", dispatcher, err)
	}
	dispatcher, err := NewToolDispatcher(map[string]tool.Tool{})
	if err != nil {
		t.Fatal(err)
	}
	toolPlans, err := NewToolPlanSource(dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	if source, sourceErr := NewToolPlanSource(nil); source != nil || sourceErr == nil || !strings.Contains(sourceErr.Error(), "source") {
		t.Fatalf("nil dispatcher source = %#v, %v", source, sourceErr)
	}
	if engine, cleanup, engineErr := NewEngine(nil, toolPlans, NewInteractionBroker(), NewExecutionPlanMetadata(), &agent.AtomicIDSource{}); engine != nil || cleanup != nil || engineErr == nil || !strings.Contains(engineErr.Error(), "provider") {
		t.Fatalf("nil provider construction = %#v, %#v, %v", engine, cleanup, engineErr)
	}
	if engine, cleanup, engineErr := NewEngine(unavailableProvider{}, nil, NewInteractionBroker(), NewExecutionPlanMetadata(), &agent.AtomicIDSource{}); engine != nil || cleanup != nil || engineErr == nil || !strings.Contains(engineErr.Error(), "plan source") {
		t.Fatalf("nil tool source construction = %#v, %#v, %v", engine, cleanup, engineErr)
	}
	if engine, cleanup, engineErr := NewEngine(unavailableProvider{}, toolPlans, nil, NewExecutionPlanMetadata(), &agent.AtomicIDSource{}); engine != nil || cleanup != nil || engineErr == nil || !strings.Contains(engineErr.Error(), "broker") {
		t.Fatalf("nil broker construction = %#v, %#v, %v", engine, cleanup, engineErr)
	}
	invalidMetadata := NewExecutionPlanMetadata()
	invalidMetadata.CompiledPlanIdentities = []string{"invalid"}
	if engine, cleanup, engineErr := NewEngine(unavailableProvider{}, toolPlans, NewInteractionBroker(), invalidMetadata, &agent.AtomicIDSource{}); engine != nil || cleanup != nil || engineErr == nil || !strings.Contains(engineErr.Error(), "compiled plan") {
		t.Fatalf("invalid plan construction = %#v, %#v, %v", engine, cleanup, engineErr)
	}
	var nilProof *Proof
	if _, runErr := nilProof.Run(t.Context()); runErr == nil || !strings.Contains(runErr.Error(), "not initialized") {
		t.Fatalf("nil Proof.Run() error = %v", runErr)
	}
	if _, runErr := (&Proof{}).Run(t.Context()); runErr == nil || !strings.Contains(runErr.Error(), "not initialized") {
		t.Fatalf("empty Proof.Run() error = %v", runErr)
	}
	if _, runErr := nilProof.RunCancellation(t.Context()); runErr == nil || !strings.Contains(runErr.Error(), "not initialized") {
		t.Fatalf("nil Proof.RunCancellation() error = %v", runErr)
	}
	proof, cleanup := newTestProof(t, unavailableProvider{}, map[string]tool.Tool{}, &ResponsesFixture{})
	dispatcher, dispatcherErr := NewToolDispatcher(map[string]tool.Tool{})
	if dispatcherErr != nil {
		t.Fatal(dispatcherErr)
	}
	if constructed, constructErr := NewProof(nil, dispatcher, &ResponsesFixture{}); constructed != nil || constructErr == nil || !strings.Contains(constructErr.Error(), "engine") {
		t.Fatalf("nil engine proof = %#v, %v", constructed, constructErr)
	}
	if constructed, constructErr := NewProof(proof.engine, nil, &ResponsesFixture{}); constructed != nil || constructErr == nil || !strings.Contains(constructErr.Error(), "dispatcher") {
		t.Fatalf("nil dispatcher proof = %#v, %v", constructed, constructErr)
	}
	if constructed, constructErr := NewProof(proof.engine, dispatcher, nil); constructed != nil || constructErr == nil || !strings.Contains(constructErr.Error(), "fixture") {
		t.Fatalf("nil fixture proof = %#v, %v", constructed, constructErr)
	}
	if proof == nil {
		t.Fatal("test proof is nil")
	}
	if _, err = proof.Run(nil); err == nil || !strings.Contains(err.Error(), "context") { //nolint:staticcheck // Boundary test proves nil is rejected before execution.
		t.Fatalf("Proof.Run(nil) error = %v", err)
	}
	if _, err = proof.RunCancellation(nil); err == nil || !strings.Contains(err.Error(), "context") { //nolint:staticcheck // Boundary test proves nil is rejected before execution.
		t.Fatalf("Proof.RunCancellation(nil) error = %v", err)
	}
	if err = cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = proof.RunCancellation(t.Context()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("closed Proof.RunCancellation() error = %v", err)
	}
}

func TestCancellationPreparationAndWaitAreBounded(t *testing.T) {
	t.Parallel()
	fixture := &ResponsesFixture{}
	if _, err := fixture.prepareCancellation(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.prepareCancellation(); err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("duplicate prepareCancellation() error = %v", err)
	}

	waitFixture := &ResponsesFixture{}
	proof, cleanup := newTestProof(t, blockingProvider{}, map[string]tool.Tool{}, waitFixture)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := proof.RunCancellation(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded Proof.RunCancellation() error = %v", err)
	}
	stopContext, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := cleanup(stopContext); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationFinalizationRejectsUnexpectedTerminalState(t *testing.T) {
	t.Parallel()
	completedEvent, err := model.Completed(model.NewUsage(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	completedProof, completedCleanup := newTestProof(
		t,
		oneEventProvider{event: completedEvent},
		map[string]tool.Tool{},
		&ResponsesFixture{},
	)
	completedRun, completedSubscription := startPlainRun(t, completedProof)
	if _, err = completedProof.finishCancellation(t.Context(), completedRun, completedSubscription); err == nil || !strings.Contains(err.Error(), "without cancellation") {
		t.Fatalf("completed finishCancellation() error = %v", err)
	}
	if err = completedCleanup(context.Background()); err != nil {
		t.Fatal(err)
	}

	cancelledProof, cancelledCleanup := newTestProof(t, blockingProvider{}, map[string]tool.Tool{}, &ResponsesFixture{})
	cancelledRun, cancelledSubscription := startPlainRun(t, cancelledProof)
	cancelledRun.Cancel()
	if _, err = cancelledProof.finishCancellation(t.Context(), cancelledRun, cancelledSubscription); err == nil || !strings.Contains(err.Error(), "did not observe") {
		t.Fatalf("unobserved finishCancellation() error = %v", err)
	}
	if err = cancelledCleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func newTestProof(
	t *testing.T,
	provider model.Provider,
	tools map[string]tool.Tool,
	fixture *ResponsesFixture,
) (*Proof, func(context.Context) error) {
	t.Helper()
	dispatcher, err := NewToolDispatcher(tools)
	if err != nil {
		t.Fatal(err)
	}
	toolPlans, err := NewToolPlanSource(dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	engine, cleanup, err := NewEngine(
		provider,
		toolPlans,
		NewInteractionBroker(),
		NewExecutionPlanMetadata(),
		&agent.AtomicIDSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewProof(engine, dispatcher, fixture)
	if err != nil {
		t.Fatal(err)
	}
	return proof, cleanup
}

func TestGeneratedExecutionPlanIsExplicitAndSourceGuaranteed(t *testing.T) {
	t.Parallel()
	metadata := NewExecutionPlanMetadata()
	if metadata.SnapshotCompatibilityIdentity != snapshotCompatibilityIdentity {
		t.Fatalf("snapshot compatibility = %q", metadata.SnapshotCompatibilityIdentity)
	}
	if len(metadata.CompiledPlanIdentities) != 8 {
		t.Fatalf("compiled identities = %v", metadata.CompiledPlanIdentities)
	}
	metadata.CompiledPlanIdentities[0] = "corrupted"
	if NewExecutionPlanMetadata().CompiledPlanIdentities[0] == "corrupted" {
		t.Fatal("execution plan metadata reused mutable backing storage")
	}
	dispatcher, err := NewToolDispatcher(map[string]tool.Tool{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewToolPlanSource(dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	first, err := source.LeaseCurrent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if releaseErr := first.Release(); releaseErr != nil {
			t.Error(releaseErr)
		}
	})
	second, err := source.LeaseGeneration(t.Context(), first.ToolPlanID())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if releaseErr := second.Release(); releaseErr != nil {
			t.Error(releaseErr)
		}
	})
	if first == second || first.ToolPlanID() != second.ToolPlanID() {
		t.Fatalf("static leases = %#v, %#v", first, second)
	}
	unknown, err := stage.NewPlanID("static:unknown")
	if err != nil {
		t.Fatal(err)
	}
	if lease, leaseErr := source.LeaseGeneration(t.Context(), unknown); lease != nil || leaseErr == nil || !strings.Contains(leaseErr.Error(), "unavailable") {
		t.Fatalf("unknown static generation = %#v, %v", lease, leaseErr)
	}
}

func TestGeneratedExecutionPlanResumesExactStaticSnapshot(t *testing.T) {
	t.Parallel()
	dispatcher, err := NewToolDispatcher(map[string]tool.Tool{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewToolPlanSource(dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	metadata := NewExecutionPlanMetadata()
	lease, err := source.LeaseCurrent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := agent.NewPlanIdentity(
		metadata.CompiledPlanIdentities,
		metadata.SnapshotCompatibilityIdentity,
		lease.ToolPlanID(),
		lease.Definitions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.Release(); err != nil {
		t.Fatal(err)
	}
	definition, err := agent.NewDefinition("snapshot-proof", "proof-model", 2)
	if err != nil {
		t.Fatal(err)
	}
	part, err := message.Text("resume the generated plan")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := message.New("snapshot-input", message.RoleUser, part)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := agent.NewSnapshot(
		"snapshot-run",
		definition,
		1,
		[]message.Message{initial},
		identity,
		2,
		agent.LifecycleSuspended,
	)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := model.Completed(model.NewUsage(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	engine, cleanup, err := NewEngine(
		oneEventProvider{event: completed},
		source,
		NewInteractionBroker(),
		metadata,
		&agent.AtomicIDSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cleanupErr := cleanup(context.Background()); cleanupErr != nil {
			t.Error(cleanupErr)
		}
	})
	run, err := engine.ResumeSnapshot(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if run.ID() != "snapshot-run" || run.ToolPlanID() != identity.ToolPlanID() {
		t.Fatalf("resumed run = %q, tool plan %q", run.ID(), run.ToolPlanID())
	}
	if err = run.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func startPlainRun(t *testing.T, proof *Proof) (*agent.Run, *event.Subscription) {
	t.Helper()
	input, err := proof.input()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := agent.NewDefinition("terminal-test", "proof-model", 1)
	if err != nil {
		t.Fatal(err)
	}
	run, err := proof.engine.Start(t.Context(), definition, input)
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := run.Subscribe(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return run, subscription
}

func TestResponsesFixtureRejectsMalformedProtocol(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, method, path, body, want string
	}{
		{name: "target", method: http.MethodGet, path: "/wrong", body: `{}`, want: "target"},
		{name: "JSON", method: http.MethodPost, path: "/v1/responses", body: `{`, want: "JSON"},
		{name: "secret", method: http.MethodPost, path: "/v1/responses", body: `{"value":"` + fixtureSecret + `"}`, want: "credential"},
		{name: "tool declaration", method: http.MethodPost, path: "/v1/responses", body: `{}`, want: "read tool"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := &ResponsesFixture{}
			response := httptest.NewRecorder()
			fixture.serveHTTP(response, fixtureRequest(test.method, test.path, test.body))
			if response.Code != http.StatusBadRequest || !strings.Contains(fixture.protocolViolation, test.want) {
				t.Fatalf("response = %d, violation = %q", response.Code, fixture.protocolViolation)
			}
		})
	}
}

func TestResponsesFixtureRejectsBadContinuationAndExtraRequest(t *testing.T) {
	t.Parallel()
	fixture := &ResponsesFixture{}
	first := httptest.NewRecorder()
	fixture.serveHTTP(first, fixtureRequest(http.MethodPost, "/v1/responses", `{"tools":[{"name":"read"}]}`))
	if first.Code != http.StatusOK || fixture.protocolViolation != "" {
		t.Fatalf("first response = %d, violation = %q", first.Code, fixture.protocolViolation)
	}
	second := httptest.NewRecorder()
	fixture.serveHTTP(second, fixtureRequest(http.MethodPost, "/v1/responses", `{}`))
	if second.Code != http.StatusBadRequest || !strings.Contains(fixture.protocolViolation, "continuation") {
		t.Fatalf("second response = %d, violation = %q", second.Code, fixture.protocolViolation)
	}

	extra := &ResponsesFixture{requests: 2}
	response := httptest.NewRecorder()
	extra.serveHTTP(response, fixtureRequest(http.MethodPost, "/v1/responses", `{}`))
	if response.Code != http.StatusBadRequest || !strings.Contains(extra.protocolViolation, "extra") {
		t.Fatalf("extra response = %d, violation = %q", response.Code, extra.protocolViolation)
	}
}

func TestResponsesFixtureRecordsStreamingWriteFailures(t *testing.T) {
	t.Parallel()
	fixture := &ResponsesFixture{}
	fixture.writeEvents(failingWriter{}, `{}`)
	if fixture.protocolViolation != "write streaming response" {
		t.Fatalf("event write violation = %q", fixture.protocolViolation)
	}
	fixture.protocolViolation = ""
	fixture.writeEvents(&boundedWriter{writes: 1}, `{}`)
	if fixture.protocolViolation != "finish streaming response" {
		t.Fatalf("terminal write violation = %q", fixture.protocolViolation)
	}
}

func fixtureRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+fixtureSecret)
	return request
}

type unavailableProvider struct{}

func (unavailableProvider) Stream(context.Context, model.Request) (model.Stream, error) {
	return nil, errors.New("unavailable")
}

type blockingProvider struct{}

func (blockingProvider) Stream(context.Context, model.Request) (model.Stream, error) {
	return blockingStream{}, nil
}

type blockingStream struct{}

func (blockingStream) Recv(ctx context.Context) (model.StreamEvent, error) {
	<-ctx.Done()
	return model.StreamEvent{}, ctx.Err()
}

func (blockingStream) Close() error { return nil }

type oneEventProvider struct{ event model.StreamEvent }

func (provider oneEventProvider) Stream(context.Context, model.Request) (model.Stream, error) {
	return &oneEventStream{event: provider.event}, nil
}

type oneEventStream struct {
	event model.StreamEvent
	done  bool
}

func (stream *oneEventStream) Recv(context.Context) (model.StreamEvent, error) {
	if stream.done {
		return model.StreamEvent{}, io.EOF
	}
	stream.done = true
	return stream.event, nil
}

func (*oneEventStream) Close() error { return nil }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type boundedWriter struct{ writes int }

func (writer *boundedWriter) Write(content []byte) (int, error) {
	if writer.writes == 0 {
		return 0, io.ErrClosedPipe
	}
	writer.writes--
	return len(content), nil
}
