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
	"github.com/spice-framework/spice-agent/model"
	"github.com/spice-framework/spice-agent/tool"
)

func TestProofConstructionAndRunRejectInvalidState(t *testing.T) {
	t.Parallel()
	if proof, cleanup, err := NewProof(nil, map[string]tool.Tool{"broken": nil}, &ResponsesFixture{}); proof != nil || cleanup != nil || err == nil || !strings.Contains(err.Error(), "dispatcher") {
		t.Fatalf("invalid tool construction = %#v, %#v, %v", proof, cleanup, err)
	}
	if proof, cleanup, err := NewProof(nil, map[string]tool.Tool{}, &ResponsesFixture{}); proof != nil || cleanup != nil || err == nil || !strings.Contains(err.Error(), "engine") {
		t.Fatalf("nil provider construction = %#v, %#v, %v", proof, cleanup, err)
	}
	var nilProof *Proof
	if _, err := nilProof.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil Proof.Run() error = %v", err)
	}
	if _, err := (&Proof{}).Run(t.Context()); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("empty Proof.Run() error = %v", err)
	}
	if _, err := nilProof.RunCancellation(t.Context()); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil Proof.RunCancellation() error = %v", err)
	}
	proof, cleanup, err := NewProof(unavailableProvider{}, map[string]tool.Tool{}, &ResponsesFixture{})
	if err != nil {
		t.Fatal(err)
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
	proof, cleanup, err := NewProof(blockingProvider{}, map[string]tool.Tool{}, waitFixture)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err = proof.RunCancellation(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded Proof.RunCancellation() error = %v", err)
	}
	stopContext, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err = cleanup(stopContext); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationFinalizationRejectsUnexpectedTerminalState(t *testing.T) {
	t.Parallel()
	completedEvent, err := model.Completed(model.NewUsage(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	completedProof, completedCleanup, err := NewProof(
		oneEventProvider{event: completedEvent},
		map[string]tool.Tool{},
		&ResponsesFixture{},
	)
	if err != nil {
		t.Fatal(err)
	}
	completedRun, completedSubscription := startPlainRun(t, completedProof)
	if _, err = completedProof.finishCancellation(t.Context(), completedRun, completedSubscription); err == nil || !strings.Contains(err.Error(), "without cancellation") {
		t.Fatalf("completed finishCancellation() error = %v", err)
	}
	if err = completedCleanup(context.Background()); err != nil {
		t.Fatal(err)
	}

	cancelledProof, cancelledCleanup, err := NewProof(blockingProvider{}, map[string]tool.Tool{}, &ResponsesFixture{})
	if err != nil {
		t.Fatal(err)
	}
	cancelledRun, cancelledSubscription := startPlainRun(t, cancelledProof)
	cancelledRun.Cancel()
	if _, err = cancelledProof.finishCancellation(t.Context(), cancelledRun, cancelledSubscription); err == nil || !strings.Contains(err.Error(), "did not observe") {
		t.Fatalf("unobserved finishCancellation() error = %v", err)
	}
	if err = cancelledCleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func startPlainRun(t *testing.T, proof *Proof) (*agent.Run, *event.Subscription) {
	t.Helper()
	input, err := architectureProofInput()
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
