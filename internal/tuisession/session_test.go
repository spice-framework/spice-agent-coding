package tuisession

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent/client"
)

const testTimeout = 2 * time.Second

type testIdentifierSource struct {
	mutex sync.Mutex
	next  int
	err   error
	value string
}

func (source *testIdentifierSource) Next() (string, error) {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	if source.err != nil {
		return "", source.err
	}
	if source.value != "" {
		return source.value, nil
	}
	source.next++
	return fmt.Sprintf("test-id-%d", source.next), nil
}

type fakeConnector struct {
	mutex      sync.Mutex
	session    client.Session
	err        error
	initialize int
}

func (connector *fakeConnector) Initialize(
	ctx context.Context,
	_ client.InitializeRequest,
) (client.Session, error) {
	connector.mutex.Lock()
	connector.initialize++
	clientSession := connector.session
	initializeErr := connector.err
	connector.mutex.Unlock()
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	default:
		return clientSession, initializeErr
	}
}

func (connector *fakeConnector) initializeCount() int {
	connector.mutex.Lock()
	defer connector.mutex.Unlock()
	return connector.initialize
}

type eventResult struct {
	frame client.EventFrame
	err   error
}

type scriptedEventStream struct {
	mutex    sync.Mutex
	results  []eventResult
	next     int
	closed   chan struct{}
	once     sync.Once
	closeErr error
}

func newEventStream(results ...eventResult) *scriptedEventStream {
	return &scriptedEventStream{results: results, closed: make(chan struct{})}
}

func (stream *scriptedEventStream) Next(ctx context.Context) (client.EventFrame, error) {
	stream.mutex.Lock()
	if stream.next < len(stream.results) {
		result := stream.results[stream.next]
		stream.next++
		stream.mutex.Unlock()
		return result.frame, result.err
	}
	stream.mutex.Unlock()
	select {
	case <-ctx.Done():
		return client.EventFrame{}, context.Cause(ctx)
	case <-stream.closed:
		return client.EventFrame{}, client.ErrClosed
	}
}

func (stream *scriptedEventStream) Close() error {
	stream.once.Do(func() { close(stream.closed) })
	return stream.closeErr
}

type interactionResult struct {
	frame client.InteractionFrame
	err   error
}

type scriptedInteractionStream struct {
	mutex    sync.Mutex
	results  []interactionResult
	next     int
	closed   chan struct{}
	once     sync.Once
	closeErr error
}

func newInteractionStream(results ...interactionResult) *scriptedInteractionStream {
	return &scriptedInteractionStream{results: results, closed: make(chan struct{})}
}

func (stream *scriptedInteractionStream) Next(ctx context.Context) (client.InteractionFrame, error) {
	stream.mutex.Lock()
	if stream.next < len(stream.results) {
		result := stream.results[stream.next]
		stream.next++
		stream.mutex.Unlock()
		return result.frame, result.err
	}
	stream.mutex.Unlock()
	select {
	case <-ctx.Done():
		return client.InteractionFrame{}, context.Cause(ctx)
	case <-stream.closed:
		return client.InteractionFrame{}, client.ErrClosed
	}
}

func (stream *scriptedInteractionStream) Close() error {
	stream.once.Do(func() { close(stream.closed) })
	return stream.closeErr
}

type fakeClientSession struct {
	clientSessionStub

	connection client.Connection

	mutex              sync.Mutex
	closed             bool
	eventCursors       []uint64
	eventStreams       []client.EventStream
	eventErrors        []error
	interactionStreams []client.InteractionStream
	interactionErrors  []error
	startCalls         int
	startResult        client.StartResult
	startErr           error
	cancelCalls        int
	cancelRequest      client.CancelRequest
	cancelResult       client.CancelResult
	respondCalls       int
	respondRequest     client.RespondRequest
	respondResult      client.RespondResult
	cancelErr          error
	respondErr         error
	closeErr           error
}

func (session *fakeClientSession) Connection() client.Connection { return session.connection }

func (session *fakeClientSession) Start(
	_ context.Context,
	_ client.StartRequest,
) (client.StartResult, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.startCalls++
	return session.startResult, session.startErr
}

func (session *fakeClientSession) Events(
	_ context.Context,
	cursor client.Cursor,
	_ client.EventStreamOptions,
) (client.EventStream, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.eventCursors = append(session.eventCursors, cursor.AfterSequence())
	index := len(session.eventCursors) - 1
	if index < len(session.eventErrors) && session.eventErrors[index] != nil {
		return nil, session.eventErrors[index]
	}
	if index >= len(session.eventStreams) {
		return nil, errors.New("unexpected event stream open")
	}
	return session.eventStreams[index], nil
}

func (session *fakeClientSession) Interactions(
	_ context.Context,
	_ client.InteractionStreamOptions,
) (client.InteractionStream, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if len(session.interactionStreams) == 0 {
		if len(session.interactionErrors) > 0 {
			err := session.interactionErrors[0]
			session.interactionErrors = session.interactionErrors[1:]
			return nil, err
		}
		return nil, errors.New("unexpected interaction stream open")
	}
	stream := session.interactionStreams[0]
	session.interactionStreams = session.interactionStreams[1:]
	return stream, nil
}

func (session *fakeClientSession) Cancel(
	_ context.Context,
	request client.CancelRequest,
) (client.CancelResult, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.cancelCalls++
	session.cancelRequest = request
	return session.cancelResult, session.cancelErr
}

func (session *fakeClientSession) Respond(
	_ context.Context,
	request client.RespondRequest,
) (client.RespondResult, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	session.respondCalls++
	session.respondRequest = request
	return session.respondResult, session.respondErr
}

func (session *fakeClientSession) Close() error {
	session.mutex.Lock()
	session.closed = true
	session.mutex.Unlock()
	return session.closeErr
}

type clientSessionStub struct{}

func (clientSessionStub) Connection() client.Connection { return client.Connection{} }

func (clientSessionStub) Start(
	context.Context,
	client.StartRequest,
) (client.StartResult, error) {
	return client.StartResult{}, errors.New("unexpected Start call")
}

func (clientSessionStub) Events(
	context.Context,
	client.Cursor,
	client.EventStreamOptions,
) (client.EventStream, error) {
	return nil, errors.New("unexpected Events call")
}

func (clientSessionStub) Interactions(
	context.Context,
	client.InteractionStreamOptions,
) (client.InteractionStream, error) {
	return nil, errors.New("unexpected Interactions call")
}

func (clientSessionStub) Cancel(
	context.Context,
	client.CancelRequest,
) (client.CancelResult, error) {
	return client.CancelResult{}, errors.New("unexpected Cancel call")
}

func (clientSessionStub) Respond(
	context.Context,
	client.RespondRequest,
) (client.RespondResult, error) {
	return client.RespondResult{}, errors.New("unexpected Respond call")
}

func (clientSessionStub) Suspend(
	context.Context,
	client.RunMutation,
) (client.SuspendResult, error) {
	return client.SuspendResult{}, errors.New("unexpected Suspend call")
}

func (clientSessionStub) Resume(
	context.Context,
	client.RunMutation,
) (client.ResumeResult, error) {
	return client.ResumeResult{}, errors.New("unexpected Resume call")
}

func (clientSessionStub) Export(context.Context, client.RunRef) (client.Snapshot, error) {
	return client.Snapshot{}, errors.New("unexpected Export call")
}

func (clientSessionStub) Import(
	context.Context,
	client.ImportRequest,
) (client.ImportResult, error) {
	return client.ImportResult{}, errors.New("unexpected Import call")
}

func (clientSessionStub) Health(context.Context) (client.Health, error) {
	return client.Health{}, errors.New("unexpected Health call")
}

func (clientSessionStub) Close() error { return nil }

func TestConstructionIsLazyAndUnusedCleanupDoesNotConnect(t *testing.T) {
	t.Parallel()
	config, _, _ := testConfig(t)
	connector := &fakeConnector{session: &fakeClientSession{}}
	uiSession, cleanup, err := NewSession(config, connector, &testIdentifierSource{})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if uiSession == nil || cleanup == nil {
		t.Fatal("NewSession() returned a nil session or cleanup")
	}
	if got := connector.initializeCount(); got != 0 {
		t.Fatalf("initialize count after construction = %d, want 0", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err = cleanup(ctx); err != nil {
		t.Fatalf("cleanup unused session: %v", err)
	}
	if got := connector.initializeCount(); got != 0 {
		t.Fatalf("initialize count after cleanup = %d, want 0", got)
	}
}

func TestSubmitReplaysEventsAfterLastPublishedSequence(t *testing.T) {
	t.Parallel()
	config, definition, connection := testConfig(t)
	run := mustRun(t, "run-replay")
	started := mustEvent(t, run, 1, client.EventRunStarted)
	completed := mustEvent(t, run, 2, client.EventRunCompleted)
	first := newEventStream(eventResult{frame: mustEventFrame(t, started)}, eventResult{err: io.EOF})
	second := newEventStream(eventResult{frame: mustEventFrame(t, completed)})
	clientSession := &fakeClientSession{
		connection:   connection,
		eventStreams: []client.EventStream{first, second},
		interactionStreams: []client.InteractionStream{newInteractionStream(
			interactionResult{frame: mustInteractionSnapshotFrame(t, connection, nil)},
			interactionResult{frame: mustInteractionControlFrame(t)},
		)},
		startResult: mustStartResult(t, run),
	}
	connector := &fakeConnector{session: clientSession}
	uiSession, cleanup, err := NewSession(config, connector, &testIdentifierSource{})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	t.Cleanup(func() { cleanupTestSession(t, cleanup) })

	initial := receiveUpdate(t, uiSession)
	if initial.Kind() != agenttui.SessionUpdateSnapshot || initial.Revision() != 1 {
		t.Fatalf("initial update = %q revision %d", initial.Kind(), initial.Revision())
	}
	prompt := mustText(t, "inspect the repository")
	intent, err := agenttui.NewIntent(agenttui.IntentSubmit, []agenttui.Text{prompt})
	if err != nil {
		t.Fatalf("construct submit intent: %v", err)
	}
	result, err := uiSession.Perform(context.Background(), intent)
	if err != nil {
		t.Fatalf("Perform(submit) error = %v", err)
	}
	if got := result.Message().String(); got != "run run-replay started" {
		t.Fatalf("submit result = %q", got)
	}

	history := receiveUpdate(t, uiSession)
	if history.Kind() != agenttui.SessionUpdatePromptHistory || history.Revision() != 2 {
		t.Fatalf("history update = %q revision %d", history.Kind(), history.Revision())
	}
	items, available := history.PromptHistory()
	if !available || len(items) != 1 || items[0].String() != prompt.String() {
		t.Fatalf("prompt history = %#v, available %v", items, available)
	}
	firstActivity := receiveUpdate(t, uiSession)
	secondActivity := receiveUpdate(t, uiSession)
	if firstActivity.Revision() != 3 || secondActivity.Revision() != 4 {
		t.Fatalf("activity revisions = %d, %d", firstActivity.Revision(), secondActivity.Revision())
	}
	firstText, firstAvailable := firstActivity.Activity()
	secondText, secondAvailable := secondActivity.Activity()
	if !firstAvailable || firstText.String() != string(client.EventRunStarted) ||
		!secondAvailable || secondText.String() != string(client.EventRunCompleted) {
		t.Fatalf("activities = %q (%v), %q (%v)", firstText.String(), firstAvailable, secondText.String(), secondAvailable)
	}

	clientSession.mutex.Lock()
	cursors := append([]uint64(nil), clientSession.eventCursors...)
	clientSession.mutex.Unlock()
	if len(cursors) != 2 || cursors[0] != 0 || cursors[1] != 1 {
		t.Fatalf("event cursors = %v, want [0 1]", cursors)
	}
	if definition != config.Definition {
		t.Fatal("test definition and configured definition differ")
	}
}

func TestInteractionSnapshotSelectsAndRespondsToPendingPrompt(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	run := mustRun(t, "run-interaction")
	pending, err := client.NewPendingInteraction(
		run,
		"approval-1",
		"confirmation",
		"Allow this operation?",
		client.NewStructuredNull(),
	)
	if err != nil {
		t.Fatalf("construct pending interaction: %v", err)
	}
	clientSession := &fakeClientSession{
		connection: connection,
		interactionStreams: []client.InteractionStream{newInteractionStream(
			interactionResult{frame: mustInteractionSnapshotFrame(t, connection, []client.PendingInteraction{pending})},
			interactionResult{frame: mustInteractionControlFrame(t)},
		)},
		respondResult: mustRespondResult(t),
	}
	uiSession, cleanup, err := NewSession(config, &fakeConnector{session: clientSession}, &testIdentifierSource{})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	t.Cleanup(func() { cleanupTestSession(t, cleanup) })
	_ = receiveUpdate(t, uiSession)
	activity := receiveUpdate(t, uiSession)
	text, available := activity.Activity()
	if !available || text.String() != "interaction: Allow this operation?" {
		t.Fatalf("interaction activity = %q, available %v", text.String(), available)
	}

	responseText := mustText(t, "yes")
	intent, err := agenttui.NewIntent(agenttui.IntentRespond, []agenttui.Text{responseText})
	if err != nil {
		t.Fatalf("construct response intent: %v", err)
	}
	result, err := uiSession.Perform(context.Background(), intent)
	if err != nil {
		t.Fatalf("Perform(respond) error = %v", err)
	}
	if got := result.Message().String(); got != "interaction response accepted" {
		t.Fatalf("response result = %q", got)
	}
	clientSession.mutex.Lock()
	request := clientSession.respondRequest
	calls := clientSession.respondCalls
	clientSession.mutex.Unlock()
	if calls != 1 || request.Run().ID() != run.ID() || request.Response().ID() != pending.ID() {
		t.Fatalf("respond request = run %q, interaction %q, calls %d", request.Run().ID(), request.Response().ID(), calls)
	}
	value, ok := request.Response().Value().Text()
	if !ok || value != responseText.String() {
		t.Fatalf("response value = %q, text %v", value, ok)
	}
}

func TestCancelUsesActiveRunAndDoesNotWaitForReceive(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	run := mustRun(t, "run-cancel")
	clientSession := &fakeClientSession{
		connection:   connection,
		eventStreams: []client.EventStream{newEventStream()},
		interactionStreams: []client.InteractionStream{newInteractionStream(
			interactionResult{frame: mustInteractionSnapshotFrame(t, connection, nil)},
			interactionResult{frame: mustInteractionControlFrame(t)},
		)},
		startResult:  mustStartResult(t, run),
		cancelResult: mustCancelResult(t),
	}
	uiSession, cleanup, err := NewSession(config, &fakeConnector{session: clientSession}, &testIdentifierSource{})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	t.Cleanup(func() { cleanupTestSession(t, cleanup) })
	_ = receiveUpdate(t, uiSession)
	submit, err := agenttui.NewIntent(agenttui.IntentSubmit, []agenttui.Text{mustText(t, "start")})
	if err != nil {
		t.Fatalf("construct submit intent: %v", err)
	}
	if _, err = uiSession.Perform(context.Background(), submit); err != nil {
		t.Fatalf("Perform(submit) error = %v", err)
	}

	receiveContext, stopReceive := context.WithCancel(context.Background())
	receiveDone := make(chan error, 1)
	go func() {
		_, receiveErr := uiSession.Receive(receiveContext)
		receiveDone <- receiveErr
	}()
	cancelIntent, err := agenttui.NewIntent(agenttui.IntentCancelActiveRun, nil)
	if err != nil {
		t.Fatalf("construct cancel intent: %v", err)
	}
	result, err := uiSession.Perform(context.Background(), cancelIntent)
	if err != nil {
		t.Fatalf("Perform(cancel) error = %v", err)
	}
	if got := result.Message().String(); got != "cancellation requested for run run-cancel" {
		t.Fatalf("cancel result = %q", got)
	}
	clientSession.mutex.Lock()
	request := clientSession.cancelRequest
	calls := clientSession.cancelCalls
	clientSession.mutex.Unlock()
	if calls != 1 || request.Run().ID() != run.ID() {
		t.Fatalf("cancel request run = %q, calls %d", request.Run().ID(), calls)
	}
	stopReceive()
	select {
	case <-time.After(testTimeout):
		t.Fatal("blocking Receive did not honor cancellation")
	case <-receiveDone:
	}
}

func TestStartFailureIsNotRetried(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	expected := errors.New("uncertain start")
	clientSession := &fakeClientSession{
		connection: connection,
		interactionStreams: []client.InteractionStream{newInteractionStream(
			interactionResult{frame: mustInteractionSnapshotFrame(t, connection, nil)},
			interactionResult{frame: mustInteractionControlFrame(t)},
		)},
		startErr: expected,
	}
	uiSession, cleanup, err := NewSession(config, &fakeConnector{session: clientSession}, &testIdentifierSource{})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	t.Cleanup(func() { cleanupTestSession(t, cleanup) })
	_ = receiveUpdate(t, uiSession)
	intent, err := agenttui.NewIntent(agenttui.IntentSubmit, []agenttui.Text{mustText(t, "start")})
	if err != nil {
		t.Fatalf("construct submit intent: %v", err)
	}
	if _, err = uiSession.Perform(context.Background(), intent); !errors.Is(err, expected) {
		t.Fatalf("Perform(submit) error = %v, want %v", err, expected)
	}
	clientSession.mutex.Lock()
	calls := clientSession.startCalls
	clientSession.mutex.Unlock()
	if calls != 1 {
		t.Fatalf("Start calls = %d, want 1", calls)
	}
}

func TestCleanupClosesStreamsAndUnblocksReceive(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	interactionStream := newInteractionStream(
		interactionResult{frame: mustInteractionSnapshotFrame(t, connection, nil)},
		interactionResult{frame: mustInteractionControlFrame(t)},
	)
	clientSession := &fakeClientSession{
		connection:         connection,
		interactionStreams: []client.InteractionStream{interactionStream},
	}
	uiSession, cleanup, err := NewSession(config, &fakeConnector{session: clientSession}, &testIdentifierSource{})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	_ = receiveUpdate(t, uiSession)
	receiveDone := make(chan error, 1)
	go func() {
		_, receiveErr := uiSession.Receive(context.Background())
		receiveDone <- receiveErr
	}()
	cleanupTestSession(t, cleanup)
	select {
	case err = <-receiveDone:
		if !errors.Is(err, client.ErrClosed) {
			t.Fatalf("Receive after cleanup error = %v, want ErrClosed", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("cleanup did not unblock Receive")
	}
	clientSession.mutex.Lock()
	closed := clientSession.closed
	clientSession.mutex.Unlock()
	if !closed {
		t.Fatal("cleanup did not close the negotiated client session")
	}
}

func TestConfigAndConstructionRejectInvalidBoundaries(t *testing.T) {
	t.Parallel()
	valid, _, _ := testConfig(t)
	tooLargeDelay := maximumReconnectDelay + time.Nanosecond
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "initialize", mutate: func(config *Config) { config.InitializeRequest = client.InitializeRequest{} }},
		{name: "definition", mutate: func(config *Config) { config.Definition = client.DefinitionRef{} }},
		{name: "workspace", mutate: func(config *Config) { config.Workspace = agenttui.WorkspaceState{} }},
		{name: "status", mutate: func(config *Config) { config.InitialStatus = agenttui.StatusState{} }},
		{name: "replay", mutate: func(config *Config) { config.ReplayLimit = 0 }},
		{name: "zero capacity", mutate: func(config *Config) { config.UpdateCapacity = 0 }},
		{name: "large capacity", mutate: func(config *Config) { config.UpdateCapacity = maximumUpdateCapacity + 1 }},
		{name: "negative reconnect", mutate: func(config *Config) { config.ReconnectDelay = -time.Nanosecond }},
		{name: "large reconnect", mutate: func(config *Config) { config.ReconnectDelay = tooLargeDelay }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := valid
			test.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
			if _, _, err := NewSession(config, &fakeConnector{}, &testIdentifierSource{}); err == nil {
				t.Fatal("NewSession() error = nil")
			}
		})
	}
	if _, _, err := NewSession(valid, nil, &testIdentifierSource{}); err == nil {
		t.Fatal("NewSession() accepted a nil connector")
	}
	if _, _, err := NewSession(valid, &fakeConnector{}, nil); err == nil {
		t.Fatal("NewSession() accepted a nil identifier source")
	}
}

func TestInitializationFailuresAreStableAndCloseResources(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	otherDefinition, err := client.NewDefinitionRef("other-agent", "revision-1")
	if err != nil {
		t.Fatalf("construct alternate definition: %v", err)
	}
	tests := []struct {
		name      string
		config    Config
		connector *fakeConnector
	}{
		{name: "connector", config: config, connector: &fakeConnector{err: errors.New("dial failed")}},
		{name: "nil session", config: config, connector: &fakeConnector{}},
		{name: "invalid connection", config: config, connector: &fakeConnector{session: &fakeClientSession{}}},
		{name: "missing definition", config: func() Config {
			value := config
			value.Definition = otherDefinition
			return value
		}(), connector: &fakeConnector{session: &fakeClientSession{connection: connection}}},
		{name: "replay over negotiated", config: func() Config {
			value := config
			value.ReplayLimit = connection.Limits().ReplayEvents() + 1
			return value
		}(), connector: &fakeConnector{session: &fakeClientSession{connection: connection}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			uiSession, cleanup, constructErr := NewSession(test.config, test.connector, &testIdentifierSource{})
			if constructErr != nil {
				t.Fatalf("NewSession() error = %v", constructErr)
			}
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			if _, receiveErr := uiSession.Receive(ctx); receiveErr == nil {
				t.Fatal("Receive() initialization error = nil")
			}
			if _, receiveErr := uiSession.Receive(ctx); receiveErr == nil {
				t.Fatal("second Receive() initialization error = nil")
			}
			cleanupTestSession(t, cleanup)
		})
	}
}

func TestReceiveAndPerformRejectInvalidState(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	interactionStream := newInteractionStream(
		interactionResult{frame: mustInteractionSnapshotFrame(t, connection, nil)},
		interactionResult{frame: mustInteractionControlFrame(t)},
	)
	clientSession := &fakeClientSession{
		connection: connection, interactionStreams: []client.InteractionStream{interactionStream},
	}
	uiSession, cleanup, err := NewSession(config, &fakeConnector{session: clientSession}, &testIdentifierSource{})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	concrete, ok := uiSession.(*Session)
	if !ok {
		t.Fatalf("session type = %T", uiSession)
	}
	t.Cleanup(func() { cleanupTestSession(t, cleanup) })
	if _, err = uiSession.Receive(nil); err == nil { //nolint:staticcheck // Boundary deliberately rejects a nil context.
		t.Fatal("Receive(nil) error = nil")
	}
	if _, err = uiSession.Perform(nil, agenttui.Intent{}); err == nil { //nolint:staticcheck // Boundary deliberately rejects a nil context.
		t.Fatal("Perform(nil) error = nil")
	}
	if _, err = uiSession.Perform(context.Background(), agenttui.Intent{}); err == nil {
		t.Fatal("Perform(invalid) error = nil")
	}
	_ = receiveUpdate(t, uiSession)
	cancelIntent, err := agenttui.NewIntent(agenttui.IntentCancelActiveRun, nil)
	if err != nil {
		t.Fatalf("construct cancel intent: %v", err)
	}
	if _, err = uiSession.Perform(context.Background(), cancelIntent); err == nil {
		t.Fatal("cancel without active run error = nil")
	}
	respondIntent, err := agenttui.NewIntent(agenttui.IntentRespond, []agenttui.Text{mustText(t, "answer")})
	if err != nil {
		t.Fatalf("construct respond intent: %v", err)
	}
	if _, err = uiSession.Perform(context.Background(), respondIntent); err == nil {
		t.Fatal("respond without interaction error = nil")
	}
	receiveContext, cancelReceive := context.WithCancel(context.Background())
	cancelReceive()
	if _, err = uiSession.Receive(receiveContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("Receive(canceled) error = %v, want context.Canceled", err)
	}
	expected := errors.New("observer failed")
	concrete.recordFailure(expected)
	if _, err = uiSession.Receive(context.Background()); !errors.Is(err, expected) {
		t.Fatalf("Receive(failure) error = %v, want %v", err, expected)
	}
	if _, err = uiSession.Receive(context.Background()); !errors.Is(err, expected) {
		t.Fatalf("Receive(delivered failure) error = %v, want %v", err, expected)
	}
}

func TestMutationFailuresAndIdempotentResults(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	run := mustRun(t, "run-mutations")
	pending, err := client.NewPendingInteraction(
		run, "approval", "confirmation", "Approve?", client.NewStructuredNull(),
	)
	if err != nil {
		t.Fatalf("construct interaction: %v", err)
	}
	clientSession := &fakeClientSession{
		connection: connection,
		interactionStreams: []client.InteractionStream{newInteractionStream(
			interactionResult{frame: mustInteractionSnapshotFrame(t, connection, []client.PendingInteraction{pending})},
			interactionResult{frame: mustInteractionControlFrame(t)},
		)},
		startResult: mustStartResult(t, run),
		cancelResult: func() client.CancelResult {
			result, resultErr := client.NewCancelResult(false, true, 1)
			if resultErr != nil {
				t.Fatalf("construct terminal cancel result: %v", resultErr)
			}
			return result
		}(),
		respondResult: func() client.RespondResult {
			result, resultErr := client.NewRespondResult(false, true)
			if resultErr != nil {
				t.Fatalf("construct duplicate response result: %v", resultErr)
			}
			return result
		}(),
		eventStreams: []client.EventStream{newEventStream()},
	}
	uiSession, cleanup, err := NewSession(config, &fakeConnector{session: clientSession}, &testIdentifierSource{})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	t.Cleanup(func() { cleanupTestSession(t, cleanup) })
	_ = receiveUpdate(t, uiSession)
	_ = receiveUpdate(t, uiSession)
	respondIntent, err := agenttui.NewIntent(agenttui.IntentRespond, []agenttui.Text{mustText(t, "yes")})
	if err != nil {
		t.Fatalf("construct response intent: %v", err)
	}
	result, err := uiSession.Perform(context.Background(), respondIntent)
	if err != nil || result.Message().String() != "interaction response was already accepted" {
		t.Fatalf("duplicate response = %q, %v", result.Message().String(), err)
	}
	submitIntent, err := agenttui.NewIntent(agenttui.IntentSubmit, []agenttui.Text{mustText(t, "work")})
	if err != nil {
		t.Fatalf("construct submit intent: %v", err)
	}
	if _, err = uiSession.Perform(context.Background(), submitIntent); err != nil {
		t.Fatalf("Perform(submit) error = %v", err)
	}
	if _, err = uiSession.Perform(context.Background(), submitIntent); err == nil {
		t.Fatal("second submit while active error = nil")
	}
	cancelIntent, err := agenttui.NewIntent(agenttui.IntentCancelActiveRun, nil)
	if err != nil {
		t.Fatalf("construct cancel intent: %v", err)
	}
	result, err = uiSession.Perform(context.Background(), cancelIntent)
	if err != nil || result.Message().String() != "run run-mutations was already terminal" {
		t.Fatalf("terminal cancel = %q, %v", result.Message().String(), err)
	}
}

func TestIdentifierAndClientMutationErrorsAreWrapped(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	tests := []struct {
		name        string
		identifiers *testIdentifierSource
		configure   func(*fakeClientSession)
	}{
		{name: "identifier source", identifiers: &testIdentifierSource{err: errors.New("entropy unavailable")}},
		{name: "invalid identifier", identifiers: &testIdentifierSource{value: " invalid "}},
		{name: "start", identifiers: &testIdentifierSource{}, configure: func(session *fakeClientSession) {
			session.startErr = errors.New("start failed")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			clientSession := &fakeClientSession{
				connection: connection,
				interactionStreams: []client.InteractionStream{newInteractionStream(
					interactionResult{frame: mustInteractionSnapshotFrame(t, connection, nil)},
					interactionResult{frame: mustInteractionControlFrame(t)},
				)},
			}
			if test.configure != nil {
				test.configure(clientSession)
			}
			uiSession, cleanup, err := NewSession(config, &fakeConnector{session: clientSession}, test.identifiers)
			if err != nil {
				t.Fatalf("NewSession() error = %v", err)
			}
			t.Cleanup(func() { cleanupTestSession(t, cleanup) })
			_ = receiveUpdate(t, uiSession)
			intent, intentErr := agenttui.NewIntent(agenttui.IntentSubmit, []agenttui.Text{mustText(t, "work")})
			if intentErr != nil {
				t.Fatalf("construct submit intent: %v", intentErr)
			}
			if _, err = uiSession.Perform(context.Background(), intent); err == nil {
				t.Fatal("Perform(submit) error = nil")
			}
		})
	}
}

func TestEventFrameValidationAndSummaries(t *testing.T) {
	t.Parallel()
	config, _, _ := testConfig(t)
	uiSession, cleanup, err := NewSession(config, &fakeConnector{}, &testIdentifierSource{})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	session, ok := uiSession.(*Session)
	if !ok {
		t.Fatalf("session type = %T", uiSession)
	}
	t.Cleanup(func() { cleanupTestSession(t, cleanup) })
	run := mustRun(t, "run-events")
	otherRun := mustRun(t, "other-run")
	if _, _, _, err = session.handleEventFrame(run, 0, client.EventFrame{}); err == nil {
		t.Fatal("empty event frame error = nil")
	}
	wrong := mustEvent(t, otherRun, 1, client.EventRunStarted)
	if _, _, _, err = session.handleEventFrame(run, 0, mustEventFrame(t, wrong)); err == nil {
		t.Fatal("wrong-run event error = nil")
	}
	skipped := mustEvent(t, run, 2, client.EventRunStarted)
	if _, _, _, err = session.handleEventFrame(run, 0, mustEventFrame(t, skipped)); err == nil {
		t.Fatal("non-contiguous event error = nil")
	}
	control, err := client.NewEventControl(1, 2, 1, 1, true, false)
	if err != nil {
		t.Fatalf("construct event control: %v", err)
	}
	controlFrame, err := client.NewEventControlFrame(control)
	if err != nil {
		t.Fatalf("construct event control frame: %v", err)
	}
	if _, _, _, err = session.handleEventFrame(run, 0, controlFrame); err == nil {
		t.Fatal("mismatched event control error = nil")
	}
	terminal, more, cursor, err := session.handleEventFrame(run, 1, controlFrame)
	if err != nil || terminal || !more || cursor != 1 {
		t.Fatalf("valid event control = terminal %v, more %v, cursor %d, error %v", terminal, more, cursor, err)
	}
	completed := mustEvent(t, run, 1, client.EventRunCompleted)
	terminal, _, cursor, err = session.handleEventFrame(run, 0, mustEventFrame(t, completed))
	if err != nil || !terminal || cursor != 1 {
		t.Fatalf("terminal event = terminal %v, cursor %d, error %v", terminal, cursor, err)
	}
}

func TestInteractionFramesEnforceSnapshotAndContiguousChanges(t *testing.T) {
	t.Parallel()
	config, _, connection := testConfig(t)
	uiSession, cleanup, err := NewSession(config, &fakeConnector{}, &testIdentifierSource{})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	session, ok := uiSession.(*Session)
	if !ok {
		t.Fatalf("session type = %T", uiSession)
	}
	t.Cleanup(func() { cleanupTestSession(t, cleanup) })
	run := mustRun(t, "run-interactions")
	first := mustPending(t, run, "first", "First?")
	second := mustPending(t, run, "second", "Second?")
	wantSnapshot := true
	if err = session.handleInteractionFrame(client.InteractionFrame{}, &wantSnapshot); err == nil {
		t.Fatal("empty interaction frame error = nil")
	}
	control, err := client.NewInteractionControl(0, 0, false, true)
	if err != nil {
		t.Fatalf("construct interaction control: %v", err)
	}
	controlFrame, err := client.NewInteractionControlFrame(control)
	if err != nil {
		t.Fatalf("construct interaction control frame: %v", err)
	}
	if err = session.handleInteractionFrame(controlFrame, &wantSnapshot); err == nil {
		t.Fatal("control before snapshot error = nil")
	}
	snapshot, err := client.NewInteractionSnapshot(0, []client.PendingInteraction{first}, connection.Limits())
	if err != nil {
		t.Fatalf("construct interaction snapshot: %v", err)
	}
	snapshotFrame, err := client.NewInteractionFrame(snapshot)
	if err != nil {
		t.Fatalf("construct interaction snapshot frame: %v", err)
	}
	if err = session.handleInteractionFrame(snapshotFrame, &wantSnapshot); err != nil {
		t.Fatalf("handle initial snapshot: %v", err)
	}
	if wantSnapshot {
		t.Fatal("snapshot did not advance stream state")
	}
	if err = session.handleInteractionFrame(snapshotFrame, &wantSnapshot); err == nil {
		t.Fatal("second snapshot error = nil")
	}
	if err = session.handleInteractionFrame(controlFrame, &wantSnapshot); err != nil {
		t.Fatalf("matching interaction control: %v", err)
	}
	opened, err := client.NewInteractionChange(client.InteractionOpened, 1, second)
	if err != nil {
		t.Fatalf("construct interaction open: %v", err)
	}
	changed, err := session.mergeInteraction(opened)
	if err != nil || changed {
		t.Fatalf("merge non-selected open = changed %v, error %v", changed, err)
	}
	if _, err = session.mergeInteraction(opened); err == nil {
		t.Fatal("non-contiguous duplicate open error = nil")
	}
	duplicate, err := client.NewInteractionChange(client.InteractionOpened, 2, second)
	if err != nil {
		t.Fatalf("construct duplicate interaction open: %v", err)
	}
	if _, err = session.mergeInteraction(duplicate); err == nil {
		t.Fatal("duplicate interaction open error = nil")
	}
	unknown := mustPending(t, run, "unknown", "Unknown?")
	closeUnknown, err := client.NewInteractionChange(client.InteractionClosed, 2, unknown)
	if err != nil {
		t.Fatalf("construct unknown interaction close: %v", err)
	}
	if _, err = session.mergeInteraction(closeUnknown); err == nil {
		t.Fatal("close-before-open error = nil")
	}
	closed, err := client.NewInteractionChange(client.InteractionClosed, 2, first)
	if err != nil {
		t.Fatalf("construct interaction close: %v", err)
	}
	changed, err = session.mergeInteraction(closed)
	if err != nil || !changed {
		t.Fatalf("merge selected close = changed %v, error %v", changed, err)
	}
	backward, err := client.NewInteractionSnapshot(1, nil, connection.Limits())
	if err != nil {
		t.Fatalf("construct backward snapshot: %v", err)
	}
	if _, err = session.mergeInteraction(backward); err == nil {
		t.Fatal("backward snapshot error = nil")
	}
	if _, err = session.mergeInteraction(client.InteractionUpdate{}); err == nil {
		t.Fatal("unsupported interaction update error = nil")
	}
}

func TestSelectionPresentationRetryAndHistoryBoundaries(t *testing.T) {
	t.Parallel()
	runA := mustRun(t, "run-a")
	runB := mustRun(t, "run-b")
	a := mustPending(t, runA, "z", "A?")
	b := mustPending(t, runB, "a", "B?")
	values := map[interactionKey]client.PendingInteraction{
		{run: runA.ID(), id: a.ID()}: a,
		{run: runB.ID(), id: b.ID()}: b,
	}
	selected, found := (interactionSelector{}).selectCurrent(values, runB, true)
	if !found || selected.ID() != b.ID() {
		t.Fatalf("active selection = %q, found %v", selected.ID(), found)
	}
	selected, found = (interactionSelector{}).selectCurrent(values, client.RunRef{}, false)
	if !found || selected.ID() != a.ID() {
		t.Fatalf("lexical selection = %q, found %v", selected.ID(), found)
	}
	if _, found = (interactionSelector{}).selectCurrent(nil, client.RunRef{}, false); found {
		t.Fatal("empty selection was found")
	}
	if !(interactionSelector{}).same(a, true, a, true) || (interactionSelector{}).same(a, true, b, true) || (interactionSelector{}).same(a, true, b, false) {
		t.Fatal("sameInteraction did not preserve identity and availability")
	}
	if !(interactionSelector{}).same(client.PendingInteraction{}, false, client.PendingInteraction{}, false) {
		t.Fatal("two absent interactions should be equal")
	}
	text, err := (eventPresentation{}).text("ok\x00\n\t")
	if err != nil || text.String() != "ok\n\t" {
		t.Fatalf("sanitized presentation = %q, %v", text.String(), err)
	}
	invalidUTF8, err := (eventPresentation{}).text(string([]byte{'a', 0xff, 'b'}))
	if err != nil || invalidUTF8.String() != "a�b" {
		t.Fatalf("UTF-8 presentation = %q, %v", invalidUTF8.String(), err)
	}
	history := make([]agenttui.Text, agenttui.MaximumPromptHistoryItems)
	for index := range history {
		history[index] = mustText(t, fmt.Sprintf("prompt-%d", index))
	}
	latest := mustText(t, "latest")
	bounded := (historyBuffer{}).append(history, latest)
	if len(bounded) != agenttui.MaximumPromptHistoryItems || bounded[len(bounded)-1].String() != latest.String() {
		t.Fatalf("bounded history length = %d, last = %q", len(bounded), bounded[len(bounded)-1].String())
	}
	config, _, _ := testConfig(t)
	uiSession, cleanup, err := NewSession(config, &fakeConnector{}, &testIdentifierSource{})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	session, ok := uiSession.(*Session)
	if !ok {
		t.Fatalf("session type = %T", uiSession)
	}
	t.Cleanup(func() { cleanupTestSession(t, cleanup) })
	if session.canRetryObservation(nil) || !session.canRetryObservation(io.EOF) || session.canRetryObservation(errors.New("fatal")) {
		t.Fatal("retry classification is incorrect")
	}
	if !session.waitToReconnect() {
		t.Fatal("zero reconnect delay should continue while open")
	}
}

func TestRandomIdentifiersAndCloseDeadline(t *testing.T) {
	t.Parallel()
	first, err := (RandomIdentifierSource{}).Next()
	if err != nil || len(first) != 32 {
		t.Fatalf("first random identifier = %q, %v", first, err)
	}
	second, err := (RandomIdentifierSource{}).Next()
	if err != nil || len(second) != 32 || first == second {
		t.Fatalf("second random identifier = %q, %v", second, err)
	}
	config, _, _ := testConfig(t)
	uiSession, cleanup, err := NewSession(config, &fakeConnector{}, &testIdentifierSource{})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	session, ok := uiSession.(*Session)
	if !ok {
		t.Fatalf("session type = %T", uiSession)
	}
	if err = session.Close(nil); err == nil { //nolint:staticcheck // Boundary deliberately rejects a nil context.
		t.Fatal("Close(nil) error = nil")
	}
	release := make(chan struct{})
	session.startWorker(func() { <-release })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = cleanup(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close(canceled) error = %v, want context.Canceled", err)
	}
	close(release)
	cleanupTestSession(t, cleanup)
	cleanupTestSession(t, cleanup)
	closedContext := closeContext{done: session.closed}
	if _, hasDeadline := closedContext.Deadline(); hasDeadline || !errors.Is(closedContext.Err(), context.Canceled) || closedContext.Value("key") != nil {
		t.Fatal("close context does not report closed state")
	}
}

func testConfig(t *testing.T) (Config, client.DefinitionRef, client.Connection) {
	t.Helper()
	protocol, err := client.NewProtocolVersion(1, 3, 0)
	if err != nil {
		t.Fatalf("construct protocol: %v", err)
	}
	protocolRange, err := client.NewProtocolRange(protocol, protocol)
	if err != nil {
		t.Fatalf("construct protocol range: %v", err)
	}
	limits, err := client.NewLimits(1<<20, 128, 512, 1<<20, 4, 4)
	if err != nil {
		t.Fatalf("construct limits: %v", err)
	}
	clientBuild, err := client.NewBuild("test-tui", "v0.1.0", "client-commit", "go1.26.5")
	if err != nil {
		t.Fatalf("construct client build: %v", err)
	}
	attempt, err := client.ParseInitializationAttemptID("11111111111111111111111111111111")
	if err != nil {
		t.Fatalf("construct initialization attempt: %v", err)
	}
	initialize, err := client.NewInitializeRequestWithAttempt(
		protocolRange,
		clientBuild,
		nil,
		nil,
		limits,
		attempt,
	)
	if err != nil {
		t.Fatalf("construct initialize request: %v", err)
	}
	definitionRef, err := client.NewDefinitionRef("default-agent", "revision-1")
	if err != nil {
		t.Fatalf("construct definition reference: %v", err)
	}
	definition, err := client.NewDefinition(definitionRef, "test-model", 16)
	if err != nil {
		t.Fatalf("construct definition: %v", err)
	}
	catalog, err := client.NewCatalog("catalog-1", []client.Definition{definition}, limits)
	if err != nil {
		t.Fatalf("construct catalog: %v", err)
	}
	health, err := client.NewHealth(client.HealthReady, nil, 0, limits)
	if err != nil {
		t.Fatalf("construct health: %v", err)
	}
	serverBuild, err := client.NewBuild("test-daemon", "v0.1.0", "server-commit", "go1.26.5")
	if err != nil {
		t.Fatalf("construct server build: %v", err)
	}
	connection, err := client.NewConnection(client.ConnectionSpec{
		Protocol: protocol, Server: serverBuild, Limits: limits, Health: health,
		ClientID: "test-client", OwnershipEpoch: 1, Catalog: catalog,
	})
	if err != nil {
		t.Fatalf("construct connection: %v", err)
	}
	workspace, err := agenttui.NewWorkspace(mustText(t, "Test workspace"), nil)
	if err != nil {
		t.Fatalf("construct workspace: %v", err)
	}
	status, err := agenttui.NewStatus(agenttui.StatusReady, mustText(t, "Connected"), nil)
	if err != nil {
		t.Fatalf("construct status: %v", err)
	}
	config, err := NewConfig(initialize, definitionRef, workspace, status)
	if err != nil {
		t.Fatalf("construct config: %v", err)
	}
	config.ReconnectDelay = 0
	return config, definitionRef, connection
}

func mustText(t *testing.T, value string) agenttui.Text {
	t.Helper()
	text, err := agenttui.NewText(value)
	if err != nil {
		t.Fatalf("construct TUI text %q: %v", value, err)
	}
	return text
}

func mustRun(t *testing.T, value string) client.RunRef {
	t.Helper()
	run, err := client.NewRunRef(value)
	if err != nil {
		t.Fatalf("construct run %q: %v", value, err)
	}
	return run
}

func mustPending(t *testing.T, run client.RunRef, id, prompt string) client.PendingInteraction {
	t.Helper()
	pending, err := client.NewPendingInteraction(run, id, "confirmation", prompt, client.NewStructuredNull())
	if err != nil {
		t.Fatalf("construct pending interaction %q: %v", id, err)
	}
	return pending
}

func mustStartResult(t *testing.T, run client.RunRef) client.StartResult {
	t.Helper()
	result, err := client.NewStartResult(run, 1, "plan-1", false)
	if err != nil {
		t.Fatalf("construct start result: %v", err)
	}
	return result
}

func mustCancelResult(t *testing.T) client.CancelResult {
	t.Helper()
	result, err := client.NewCancelResult(true, false, 0)
	if err != nil {
		t.Fatalf("construct cancel result: %v", err)
	}
	return result
}

func mustRespondResult(t *testing.T) client.RespondResult {
	t.Helper()
	result, err := client.NewRespondResult(true, false)
	if err != nil {
		t.Fatalf("construct respond result: %v", err)
	}
	return result
}

func mustEvent(
	t *testing.T,
	run client.RunRef,
	sequence uint64,
	kind client.EventKind,
) client.Event {
	t.Helper()
	detail := client.NoEventDetail()
	var err error
	if kind == client.EventRunStarted {
		detail, err = client.NewRunStartedDetail("default-agent")
	}
	if err != nil {
		t.Fatalf("construct event detail: %v", err)
	}
	event, err := client.NewEvent(run, sequence, time.Unix(int64(sequence), 0), kind, detail)
	if err != nil {
		t.Fatalf("construct event: %v", err)
	}
	return event
}

func mustEventFrame(t *testing.T, event client.Event) client.EventFrame {
	t.Helper()
	frame, err := client.NewEventFrame(event)
	if err != nil {
		t.Fatalf("construct event frame: %v", err)
	}
	return frame
}

func mustInteractionSnapshotFrame(
	t *testing.T,
	connection client.Connection,
	pending []client.PendingInteraction,
) client.InteractionFrame {
	t.Helper()
	update, err := client.NewInteractionSnapshot(0, pending, connection.Limits())
	if err != nil {
		t.Fatalf("construct interaction snapshot: %v", err)
	}
	frame, err := client.NewInteractionFrame(update)
	if err != nil {
		t.Fatalf("construct interaction snapshot frame: %v", err)
	}
	return frame
}

func mustInteractionControlFrame(t *testing.T) client.InteractionFrame {
	t.Helper()
	control, err := client.NewInteractionControl(0, 0, false, true)
	if err != nil {
		t.Fatalf("construct interaction control: %v", err)
	}
	frame, err := client.NewInteractionControlFrame(control)
	if err != nil {
		t.Fatalf("construct interaction control frame: %v", err)
	}
	return frame
}

func receiveUpdate(t *testing.T, session agenttui.Session) agenttui.SessionUpdate {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	update, err := session.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	return update
}

func cleanupTestSession(t *testing.T, cleanup func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := cleanup(ctx); err != nil {
		t.Fatalf("cleanup session: %v", err)
	}
}

var (
	_ client.Connector         = (*fakeConnector)(nil)
	_ client.Session           = (*fakeClientSession)(nil)
	_ client.EventStream       = (*scriptedEventStream)(nil)
	_ client.InteractionStream = (*scriptedInteractionStream)(nil)
)
