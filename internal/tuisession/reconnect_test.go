package tuisession

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/spice-framework/spice-agent/client"
)

type reconnectOutcome struct {
	session client.Session
	err     error
}

type reconnectConnector struct {
	mutex    sync.Mutex
	outcomes []reconnectOutcome
	requests []client.InitializeRequest
}

func (connector *reconnectConnector) Initialize(
	ctx context.Context,
	request client.InitializeRequest,
) (client.Session, error) {
	connector.mutex.Lock()
	defer connector.mutex.Unlock()
	connector.requests = append(connector.requests, request)
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if len(connector.outcomes) == 0 {
		return nil, errors.New("unexpected reconnect initialization")
	}
	result := connector.outcomes[0]
	connector.outcomes = connector.outcomes[1:]
	return result.session, result.err
}

func (connector *reconnectConnector) capturedRequests() []client.InitializeRequest {
	connector.mutex.Lock()
	defer connector.mutex.Unlock()
	return append([]client.InitializeRequest(nil), connector.requests...)
}

func TestRetryObservationRestoresExactReconnectClaim(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	current := &fakeClientSession{connection: connection}
	replacement := &fakeClientSession{connection: connection}
	connector := &reconnectConnector{outcomes: []reconnectOutcome{{session: replacement}}}
	session := reconnectTestSession(t, config, connector, current)

	announced := false
	if !session.retryObservation("event", 17, 1, client.ErrClosed, &announced) {
		t.Fatal("retryObservation() = false")
	}
	if !announced {
		t.Fatal("event reconnect was not announced")
	}
	requests := connector.capturedRequests()
	if len(requests) != 1 {
		t.Fatalf("initialize requests = %d", len(requests))
	}
	claim, available := requests[0].Reconnect()
	if !available || claim.ClientID() != connection.ClientID() ||
		claim.ExpectedEpoch() != connection.OwnershipEpoch() {
		t.Fatalf("reconnect claim = %#v, available %v", claim, available)
	}
	session.clientMutex.RLock()
	active := session.clientSession
	generation := session.clientGeneration
	session.clientMutex.RUnlock()
	current.mutex.Lock()
	currentClosed := current.closed
	current.mutex.Unlock()
	if active != replacement || generation != 2 || !currentClosed {
		t.Fatalf("restored client = %T generation %d current closed %v", active, generation, currentClosed)
	}
	first := <-session.updates
	second := <-session.updates
	firstActivity, firstAvailable := first.update.Activity()
	secondActivity, secondAvailable := second.update.Activity()
	if !firstAvailable || !strings.Contains(firstActivity.String(), "sequence 17") ||
		!secondAvailable || secondActivity.String() != "daemon connection restored" {
		t.Fatalf("reconnect activities = %q (%v), %q (%v)",
			firstActivity.String(), firstAvailable, secondActivity.String(), secondAvailable)
	}
	if session.retryObservation("event", 18, generation, errors.New("permanent"), &announced) {
		t.Fatal("permanent observation failure was retried")
	}
}

func TestRestoreFallsBackToFreshSessionOnlyWithoutActiveRun(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	unavailable := reconnectStatusError(t, client.ErrorUnavailable, true)
	current := &fakeClientSession{connection: connection}
	fresh := &fakeClientSession{connection: connection}
	connector := &reconnectConnector{outcomes: []reconnectOutcome{
		{err: unavailable},
		{session: fresh},
	}}
	session := reconnectTestSession(t, config, connector, current)
	session.pending[interactionKey{run: "old-run", id: "old-interaction"}] = client.PendingInteraction{}
	session.interactionRevision = 4
	if err := session.restoreConnection(1); err != nil {
		t.Fatalf("restoreConnection() error = %v", err)
	}
	requests := connector.capturedRequests()
	if len(requests) != 2 {
		t.Fatalf("initialize requests = %d", len(requests))
	}
	if _, reconnect := requests[0].Reconnect(); !reconnect {
		t.Fatal("first request is not a reconnect")
	}
	if _, reconnect := requests[1].Reconnect(); reconnect {
		t.Fatal("fresh fallback retained a reconnect claim")
	}
	if len(session.pending) != 0 || session.interactionRevision != 0 {
		t.Fatal("fresh fallback retained daemon-owned interaction state")
	}
	update := <-session.updates
	activity, available := update.update.Activity()
	if !available || activity.String() != "daemon connection restored with a fresh session" {
		t.Fatalf("fresh restore activity = %q, available %v", activity.String(), available)
	}

	activeCurrent := &fakeClientSession{connection: connection}
	activeConnector := &reconnectConnector{outcomes: []reconnectOutcome{{err: unavailable}}}
	active := reconnectTestSession(t, config, activeConnector, activeCurrent)
	active.hasActiveRun = true
	active.activeRun = mustRun(t, "active-run")
	if err := active.restoreConnection(1); err == nil || !strings.Contains(err.Error(), "active run") {
		t.Fatalf("active-run restore error = %v", err)
	}
}

func TestRestoreRejectsInvalidAndSupersededCandidates(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)

	staleConnector := &reconnectConnector{}
	stale := reconnectTestSession(t, config, staleConnector, &fakeClientSession{connection: connection})
	stale.clientGeneration = 2
	if err := stale.restoreConnection(1); err != nil || len(staleConnector.capturedRequests()) != 0 {
		t.Fatalf("stale restore = %v with %d requests", err, len(staleConnector.capturedRequests()))
	}

	nilConnector := &reconnectConnector{outcomes: []reconnectOutcome{{}}}
	nilCandidate := reconnectTestSession(t, config, nilConnector, &fakeClientSession{connection: connection})
	if err := nilCandidate.restoreConnection(1); err == nil || !strings.Contains(err.Error(), "nil restored") {
		t.Fatalf("nil candidate error = %v", err)
	}

	invalid := &fakeClientSession{}
	invalidConnector := &reconnectConnector{outcomes: []reconnectOutcome{{session: invalid}}}
	invalidCandidate := reconnectTestSession(t, config, invalidConnector, &fakeClientSession{connection: connection})
	if err := invalidCandidate.restoreConnection(1); err == nil || !strings.Contains(err.Error(), "negotiated connection") {
		t.Fatalf("invalid candidate error = %v", err)
	}
	invalid.mutex.Lock()
	invalidClosed := invalid.closed
	invalid.mutex.Unlock()
	if !invalidClosed {
		t.Fatal("invalid replacement client was not closed")
	}

	missing := reconnectTestSession(t, config, &reconnectConnector{}, nil)
	if err := missing.restoreConnection(1); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing current client error = %v", err)
	}
}

func TestReconnectRetryClassificationAndClosedState(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	session := reconnectTestSession(t, config, &reconnectConnector{}, &fakeClientSession{connection: connection})
	unavailableRetry := reconnectStatusError(t, client.ErrorUnavailable, true)
	unavailableFinal := reconnectStatusError(t, client.ErrorUnavailable, false)
	unauthenticated := reconnectStatusError(t, client.ErrorUnauthenticated, true)
	internal := reconnectStatusError(t, client.ErrorInternal, false)
	facts, err := client.NewErrorFacts("replay initialization", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	attempt, available := config.InitializeRequest.AttemptID()
	if !available {
		t.Fatal("test initialize request has no attempt ID")
	}
	replay, err := client.NewInitializationReplayError(facts, attempt)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name             string
		err              error
		retryUnavailable bool
		want             bool
	}{
		{name: "replay", err: replay, want: true},
		{name: "unavailable accepted", err: unavailableRetry, retryUnavailable: true, want: true},
		{name: "unavailable rejected", err: unavailableRetry, want: false},
		{name: "unavailable final", err: unavailableFinal, retryUnavailable: true, want: false},
		{name: "unauthenticated", err: unauthenticated, want: true},
		{name: "internal", err: internal, want: false},
		{name: "transport", err: io.ErrUnexpectedEOF, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := session.retryInitialization(test.err, test.retryUnavailable); got != test.want {
				t.Fatalf("retryInitialization() = %v, want %v", got, test.want)
			}
		})
	}
	if !session.reconnectSessionUnavailable(unavailableRetry) ||
		session.reconnectSessionUnavailable(unavailableFinal) || session.reconnectSessionUnavailable(internal) {
		t.Fatal("reconnect unavailable classification is incorrect")
	}
	if session.closedForRestore() {
		t.Fatal("new session reports closed")
	}
	close(session.closed)
	if !session.closedForRestore() || session.waitToReconnect() {
		t.Fatal("closed session permits restore")
	}
	announced := false
	if session.retryObservation("interaction", 0, 1, io.EOF, &announced) {
		t.Fatal("closed observation was retried")
	}
}

func TestSubmitRetriesOnceOnlyAfterFreshDaemonRestore(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	unavailable := reconnectStatusError(t, client.ErrorUnavailable, true)
	run := mustRun(t, "fresh-submit")
	current := &fakeClientSession{connection: connection, startErr: unavailable}
	fresh := &fakeClientSession{
		connection:  connection,
		startResult: mustStartResult(t, run),
		eventStreams: []client.EventStream{newEventStream(
			eventResult{frame: mustEventFrame(t, mustEvent(t, run, 1, client.EventRunCompleted))},
		)},
	}
	connector := &reconnectConnector{outcomes: []reconnectOutcome{{err: unavailable}, {session: fresh}}}
	session := reconnectTestSession(t, config, connector, current)
	t.Cleanup(func() { cleanupTestSession(t, session.Close) })

	result, err := session.performSubmit(context.Background(), mustText(t, "one prompt"))
	if err != nil || !strings.Contains(result.Message().String(), run.ID()) {
		t.Fatalf("fresh submit result = %q, error %v", result.Message().String(), err)
	}
	current.mutex.Lock()
	currentCalls := current.startCalls
	firstRequest := current.startRequests[0]
	current.mutex.Unlock()
	fresh.mutex.Lock()
	freshCalls := fresh.startCalls
	secondRequest := fresh.startRequests[0]
	fresh.mutex.Unlock()
	if currentCalls != 1 || freshCalls != 1 || firstRequest.Operation() != secondRequest.Operation() ||
		firstRequest.Input().MessageID() != secondRequest.Input().MessageID() ||
		firstRequest.Input().Text() != secondRequest.Input().Text() {
		t.Fatalf("fresh retry calls old/new=%d/%d or changed operation/input identity", currentCalls, freshCalls)
	}
	first := <-session.updates
	second := <-session.updates
	activity, activityOK := first.update.Activity()
	history, historyOK := second.update.PromptHistory()
	if !activityOK || activity.String() != "daemon connection restored with a fresh session" ||
		!historyOK || len(history) != 1 || history[0].String() != "one prompt" {
		t.Fatalf("fresh submit updates activity=%q/%v history=%v/%v", activity.String(), activityOK, history, historyOK)
	}
}

func TestSubmitFreshRetryIsFailClosed(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	unavailable := reconnectStatusError(t, client.ErrorUnavailable, true)
	acceptedRun := mustRun(t, "accepted-once")
	tests := []struct {
		name             string
		startResult      client.StartResult
		startErr         error
		connector        *reconnectConnector
		cancelContext    bool
		wantRestoreCalls int
		wantError        bool
	}{
		{
			name: "accepted operation is not retried", startResult: mustStartResult(t, acceptedRun),
			connector: &reconnectConnector{},
		},
		{
			name: "ordinary error is not retried", startErr: errors.New("provider rejected request"),
			connector: &reconnectConnector{}, wantError: true,
		},
		{
			name:      "operation correlated unavailable is not retried",
			startErr:  reconnectStatusErrorWithOperation(t, client.ErrorUnavailable, true),
			connector: &reconnectConnector{}, wantError: true,
		},
		{
			name: "uncertain operation is not retried", startErr: reconnectUncertainError(t),
			connector: &reconnectConnector{}, wantError: true,
		},
		{
			name: "restore failure is not retried", startErr: unavailable,
			connector:        &reconnectConnector{outcomes: []reconnectOutcome{{err: reconnectStatusError(t, client.ErrorInternal, false)}}},
			wantRestoreCalls: 1, wantError: true,
		},
		{
			name: "same daemon reconnect is not retried", startErr: unavailable,
			connector:        &reconnectConnector{outcomes: []reconnectOutcome{{session: &fakeClientSession{connection: connection}}}},
			wantRestoreCalls: 1, wantError: true,
		},
		{
			name: "cancelled caller is not retried", startErr: unavailable,
			connector: &reconnectConnector{}, cancelContext: true, wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			current := &fakeClientSession{
				connection: connection, startResult: test.startResult, startErr: test.startErr,
			}
			session := reconnectTestSession(t, config, test.connector, current)
			ctx := context.Background()
			if test.cancelContext {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := session.performSubmit(ctx, mustText(t, "bounded prompt"))
			if (err != nil) != test.wantError {
				t.Fatalf("perform submit error = %v, want error %v", err, test.wantError)
			}
			current.mutex.Lock()
			calls := current.startCalls
			current.mutex.Unlock()
			if calls != 1 || len(test.connector.capturedRequests()) != test.wantRestoreCalls {
				t.Fatalf("start/restore calls = %d/%d, want 1/%d", calls, len(test.connector.capturedRequests()), test.wantRestoreCalls)
			}
		})
	}
}

func reconnectTestSession(
	t *testing.T,
	config Config,
	connector client.Connector,
	current client.Session,
) *Session {
	t.Helper()
	adapted, _, err := NewSession(config, connector, &testIdentifierSource{})
	if err != nil {
		t.Fatal(err)
	}
	adapted.clientSession = current
	adapted.clientGeneration = 1
	return adapted
}

func reconnectStatusErrorWithOperation(t *testing.T, code client.ErrorCode, retryable bool) error {
	t.Helper()
	operation, err := client.NewOperationID("correlated-operation")
	if err != nil {
		t.Fatal(err)
	}
	facts, err := client.NewErrorFacts("correlated status", retryable, &operation)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.NewStatusError(code, facts)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func reconnectUncertainError(t *testing.T) error {
	t.Helper()
	operation, err := client.NewOperationID("uncertain-operation")
	if err != nil {
		t.Fatal(err)
	}
	facts, err := client.NewErrorFacts("uncertain start", true, &operation)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.NewUncertainOperationError(facts, operation, "start")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func reconnectStatusError(t *testing.T, code client.ErrorCode, retryable bool) error {
	t.Helper()
	facts, err := client.NewErrorFacts("test status", retryable, nil)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.NewStatusError(code, facts)
	if err != nil {
		t.Fatal(err)
	}
	return status
}
