package tuisession

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice/lifecycle"
)

const (
	defaultReplayLimit    = 256
	defaultUpdateCapacity = 64
	defaultReconnectDelay = 50 * time.Millisecond
	maximumUpdateCapacity = 1024
	maximumReconnectDelay = 5 * time.Second
)

// IdentifierSource supplies unique, bounded identifiers for client mutations
// and input messages. Implementations must be safe for concurrent use.
type IdentifierSource interface {
	Next() (string, error)
}

// RandomIdentifierSource produces cryptographically random identifiers without
// mutable package state.
type RandomIdentifierSource struct{}

// Next returns one lowercase 128-bit hexadecimal identifier.
func (RandomIdentifierSource) Next() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", fmt.Errorf("read random session identifier: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

// Config contains immutable construction inputs for a TUI client adapter.
// ReplayLimit and UpdateCapacity are validated again against the negotiated
// connection before any observation stream opens.
type Config struct {
	InitializeRequest client.InitializeRequest
	Definition        client.DefinitionRef
	Workspace         agenttui.WorkspaceState
	InitialStatus     agenttui.StatusState
	ReplayLimit       uint32
	UpdateCapacity    uint32
	ReconnectDelay    time.Duration
}

// NewConfig constructs production defaults for a bounded TUI adapter.
func NewConfig(
	initialize client.InitializeRequest,
	definition client.DefinitionRef,
	workspace agenttui.WorkspaceState,
	status agenttui.StatusState,
) (Config, error) {
	config := Config{
		InitializeRequest: initialize,
		Definition:        definition,
		Workspace:         workspace,
		InitialStatus:     status,
		ReplayLimit:       defaultReplayLimit,
		UpdateCapacity:    defaultUpdateCapacity,
		ReconnectDelay:    defaultReconnectDelay,
	}
	return config, config.Validate()
}

// Validate reports whether construction inputs are complete and bounded.
func (config Config) Validate() error {
	if err := config.InitializeRequest.Validate(); err != nil {
		return fmt.Errorf("initialize request: %w", err)
	}
	if err := config.Definition.Validate(); err != nil {
		return fmt.Errorf("definition: %w", err)
	}
	if err := config.Workspace.Validate(); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if err := config.InitialStatus.Validate(); err != nil {
		return fmt.Errorf("initial status: %w", err)
	}
	if config.ReplayLimit == 0 {
		return errors.New("event replay limit must be positive")
	}
	if config.UpdateCapacity == 0 || config.UpdateCapacity > maximumUpdateCapacity {
		return fmt.Errorf("update capacity must be between 1 and %d", maximumUpdateCapacity)
	}
	if config.ReconnectDelay < 0 || config.ReconnectDelay > maximumReconnectDelay {
		return fmt.Errorf("reconnect delay must be between zero and %s", maximumReconnectDelay)
	}
	return nil
}

type delivery struct {
	update agenttui.SessionUpdate
	err    error
}

type interactionKey struct {
	run string
	id  string
}

type closeContext struct{ done <-chan struct{} }

func (closeContext) Deadline() (time.Time, bool)   { return time.Time{}, false }
func (current closeContext) Done() <-chan struct{} { return current.done }
func (current closeContext) Err() error {
	select {
	case <-current.done:
		return context.Canceled
	default:
		return nil
	}
}
func (closeContext) Value(any) any { return nil }

// Session is the lifecycle-owned adapter implementation. Callers should depend
// on agenttui.Session; the concrete type is exported only to make generated Go
// and diagnostics intelligible.
type Session struct {
	config      Config
	connector   client.Connector
	identifiers IdentifierSource

	closed         chan struct{}
	initializeOnce sync.Once
	initializeDone chan struct{}

	clientMutex      sync.RWMutex
	clientSession    client.Session
	clientGeneration uint64
	initializeErr    error
	reconnectMutex   sync.Mutex

	publishMutex sync.Mutex
	revision     uint64
	updates      chan delivery

	stateMutex          sync.Mutex
	activeRun           client.RunRef
	hasActiveRun        bool
	eventCursor         uint64
	promptHistory       []agenttui.Text
	pending             map[interactionKey]client.PendingInteraction
	interactionRevision uint64

	ordinaryMutex   sync.Mutex
	cancelMutex     sync.Mutex
	identifierMutex sync.Mutex

	streamMutex       sync.Mutex
	eventStream       client.EventStream
	interactionStream client.InteractionStream

	workersMutex  sync.Mutex
	workersClosed bool
	workers       sync.WaitGroup

	failureMutex     sync.RWMutex
	failure          error
	failureDelivered bool

	closeOnce   sync.Once
	closeDone   chan struct{}
	closeResult error
}

// New creates an I/O-lazy TUI session and lifecycle cleanup. It never resolves
// an endpoint, starts a daemon, or opens a connection during construction.
func New(
	config Config,
	connector client.Connector,
	identifiers IdentifierSource,
) (agenttui.Session, lifecycle.Cleanup, error) {
	if err := config.Validate(); err != nil {
		return nil, nil, fmt.Errorf("construct TUI session: %w", err)
	}
	if connector == nil {
		return nil, nil, errors.New("construct TUI session: connector must not be nil")
	}
	if identifiers == nil {
		return nil, nil, errors.New("construct TUI session: identifier source must not be nil")
	}
	session := &Session{
		config: config, connector: connector, identifiers: identifiers,
		closed: make(chan struct{}), initializeDone: make(chan struct{}),
		updates: make(chan delivery, config.UpdateCapacity),
		pending: make(map[interactionKey]client.PendingInteraction), closeDone: make(chan struct{}),
	}
	return session, session.Close, nil
}

// Receive returns the next strictly increasing presentation update.
func (session *Session) Receive(ctx context.Context) (agenttui.SessionUpdate, error) {
	if ctx == nil {
		return agenttui.SessionUpdate{}, errors.New("receive TUI update: context must not be nil")
	}
	if err := session.ensureInitialized(ctx); err != nil {
		return agenttui.SessionUpdate{}, err
	}
	if err := session.deliveredFailure(); err != nil {
		return agenttui.SessionUpdate{}, err
	}
	select {
	case <-ctx.Done():
		return agenttui.SessionUpdate{}, context.Cause(ctx)
	case <-session.closed:
		return agenttui.SessionUpdate{}, client.ErrClosed
	case next := <-session.updates:
		if next.err != nil {
			session.markFailureDelivered()
			return agenttui.SessionUpdate{}, next.err
		}
		return next.update, nil
	}
}

// Perform translates one TUI intent into exactly one client mutation. Start,
// respond, and cancel are never retried by this adapter.
func (session *Session) Perform(ctx context.Context, intent agenttui.Intent) (agenttui.CommandResult, error) {
	if ctx == nil {
		return agenttui.CommandResult{}, errors.New("perform TUI intent: context must not be nil")
	}
	if err := intent.Validate(); err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("perform TUI intent: %w", err)
	}
	if err := session.ensureInitialized(ctx); err != nil {
		return agenttui.CommandResult{}, err
	}
	switch intent.Kind() {
	case agenttui.IntentCancelActiveRun:
		session.cancelMutex.Lock()
		defer session.cancelMutex.Unlock()
		return session.performCancel(ctx)
	case agenttui.IntentSubmit:
		session.ordinaryMutex.Lock()
		defer session.ordinaryMutex.Unlock()
		return session.performSubmit(ctx, intent.Values()[0])
	case agenttui.IntentRespond:
		session.ordinaryMutex.Lock()
		defer session.ordinaryMutex.Unlock()
		return session.performRespond(ctx, intent.Values()[0])
	default:
		return agenttui.CommandResult{}, fmt.Errorf("perform TUI intent: unsupported kind %q", intent.Kind())
	}
}

func (session *Session) ensureInitialized(ctx context.Context) error {
	select {
	case <-session.closed:
		return client.ErrClosed
	default:
	}
	session.initializeOnce.Do(func() { go session.initialize() })
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-session.closed:
		return client.ErrClosed
	case <-session.initializeDone:
		session.clientMutex.RLock()
		err := session.initializeErr
		session.clientMutex.RUnlock()
		if err != nil {
			return fmt.Errorf("initialize TUI session: %w", err)
		}
		return nil
	}
}

func (session *Session) initialize() {
	defer close(session.initializeDone)
	clientSession, err := session.connector.Initialize(session.context(), session.config.InitializeRequest)
	if err != nil {
		session.setInitializeError(err)
		return
	}
	if clientSession == nil {
		session.setInitializeError(errors.New("connector returned a nil client session"))
		return
	}
	if err = session.validateConnection(clientSession.Connection()); err != nil {
		session.setInitializeError(err)
		return
	}
	select {
	case <-session.closed:
		closeErr := clientSession.Close()
		session.clientMutex.Lock()
		session.initializeErr = errors.Join(client.ErrClosed, closeErr)
		session.clientMutex.Unlock()
		return
	default:
	}
	session.clientMutex.Lock()
	session.clientSession = clientSession
	session.clientGeneration = 1
	session.clientMutex.Unlock()
	if err = session.publishSnapshot(); err != nil {
		session.clientMutex.Lock()
		session.initializeErr = err
		session.clientMutex.Unlock()
		closeErr := clientSession.Close()
		if closeErr != nil {
			session.recordFailure(fmt.Errorf("close client after initial snapshot failure: %w", closeErr))
		}
		return
	}
	session.startWorker(session.observeInteractions)
}

func (session *Session) setInitializeError(err error) {
	session.clientMutex.Lock()
	session.initializeErr = err
	session.clientMutex.Unlock()
}

func (session *Session) validateConnection(connection client.Connection) error {
	if err := connection.Validate(); err != nil {
		return fmt.Errorf("negotiated connection: %w", err)
	}
	if session.config.ReplayLimit > connection.Limits().ReplayEvents() {
		return fmt.Errorf(
			"event replay limit %d exceeds negotiated limit %d",
			session.config.ReplayLimit,
			connection.Limits().ReplayEvents(),
		)
	}
	if _, found := connection.Catalog().Find(session.config.Definition); !found {
		return fmt.Errorf(
			"definition %s@%s is not advertised by the negotiated server",
			session.config.Definition.ID(),
			session.config.Definition.Revision(),
		)
	}
	return nil
}

func (session *Session) publishSnapshot() error {
	return session.publish(func(revision uint64) (agenttui.SessionUpdate, error) {
		snapshot, err := agenttui.NewSessionSnapshot(
			revision,
			session.config.Workspace,
			session.config.InitialStatus,
			nil,
			nil,
		)
		if err != nil {
			return agenttui.SessionUpdate{}, err
		}
		return agenttui.NewSnapshotUpdate(snapshot)
	})
}

func (session *Session) publish(
	build func(uint64) (agenttui.SessionUpdate, error),
) error {
	session.publishMutex.Lock()
	defer session.publishMutex.Unlock()
	if session.revision == ^uint64(0) {
		return errors.New("TUI session revision exhausted")
	}
	nextRevision := session.revision + 1
	update, err := build(nextRevision)
	if err != nil {
		return fmt.Errorf("build TUI update: %w", err)
	}
	select {
	case <-session.closed:
		return client.ErrClosed
	case session.updates <- delivery{update: update}:
		session.revision = nextRevision
		return nil
	}
}

func (session *Session) publishActivity(value string) error {
	text, err := presentationText(value)
	if err != nil {
		return err
	}
	return session.publish(func(revision uint64) (agenttui.SessionUpdate, error) {
		return agenttui.NewActivityUpdate(revision, text)
	})
}

func (session *Session) publishHistory(history []agenttui.Text) error {
	return session.publish(func(revision uint64) (agenttui.SessionUpdate, error) {
		return agenttui.NewPromptHistoryUpdate(revision, history)
	})
}

func (session *Session) performSubmit(
	ctx context.Context,
	prompt agenttui.Text,
) (agenttui.CommandResult, error) {
	if _, err := agenttui.NewEditor(prompt.String()); err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("submit prompt: %w", err)
	}
	session.stateMutex.Lock()
	active := session.hasActiveRun
	session.stateMutex.Unlock()
	if active {
		return agenttui.CommandResult{}, errors.New("submit prompt: a run is already active")
	}
	operation, err := session.newOperationID()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("submit prompt: %w", err)
	}
	messageID, err := session.nextIdentifier()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("submit prompt: create message ID: %w", err)
	}
	input, err := client.NewInput(messageID, prompt.String())
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("submit prompt: %w", err)
	}
	request, err := client.NewStartRequest(operation, session.config.Definition, input)
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("submit prompt: %w", err)
	}
	clientSession := session.currentClient()
	operationContext, cancel := session.operationContext(ctx)
	result, err := clientSession.Start(operationContext, request)
	cancel()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("submit prompt: %w", err)
	}
	session.stateMutex.Lock()
	session.activeRun = result.Run()
	session.hasActiveRun = true
	session.eventCursor = 0
	session.promptHistory = appendBoundedHistory(session.promptHistory, prompt)
	history := slices.Clone(session.promptHistory)
	session.stateMutex.Unlock()
	if err = session.publishHistory(history); err != nil && !errors.Is(err, client.ErrClosed) {
		return agenttui.CommandResult{}, fmt.Errorf("publish submitted prompt: %w", err)
	}
	session.startWorker(func() { session.observeEvents(result.Run()) })
	return commandResult("run " + result.Run().ID() + " started")
}

func (session *Session) performCancel(ctx context.Context) (agenttui.CommandResult, error) {
	session.stateMutex.Lock()
	run, active := session.activeRun, session.hasActiveRun
	session.stateMutex.Unlock()
	if !active {
		return agenttui.CommandResult{}, errors.New("cancel run: no run is active")
	}
	operation, err := session.newOperationID()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("cancel run: %w", err)
	}
	request, err := client.NewCancelRequest(run, operation, "cancelled from TUI")
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("cancel run: %w", err)
	}
	operationContext, cancel := session.operationContext(ctx)
	result, err := session.currentClient().Cancel(operationContext, request)
	cancel()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("cancel run: %w", err)
	}
	if result.AlreadyTerminal() {
		session.clearActiveRun(run)
		return commandResult("run " + run.ID() + " was already terminal")
	}
	return commandResult("cancellation requested for run " + run.ID())
}

func (session *Session) performRespond(
	ctx context.Context,
	value agenttui.Text,
) (agenttui.CommandResult, error) {
	pending, found := session.currentInteraction()
	if !found {
		return agenttui.CommandResult{}, errors.New("respond to interaction: no interaction is pending")
	}
	structured, err := client.NewStructuredText(value.String())
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("respond to interaction: %w", err)
	}
	response, err := client.NewInteractionResponse(pending.ID(), structured)
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("respond to interaction: %w", err)
	}
	operation, err := session.newOperationID()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("respond to interaction: %w", err)
	}
	request, err := client.NewRespondRequest(pending.Run(), operation, response)
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("respond to interaction: %w", err)
	}
	operationContext, cancel := session.operationContext(ctx)
	result, err := session.currentClient().Respond(operationContext, request)
	cancel()
	if err != nil {
		return agenttui.CommandResult{}, fmt.Errorf("respond to interaction: %w", err)
	}
	if result.DuplicateOperation() {
		return commandResult("interaction response was already accepted")
	}
	return commandResult("interaction response accepted")
}

func (session *Session) observeEvents(run client.RunRef) {
	cursor := uint64(0)
	reconnecting := false
	for {
		stream, generation, err := session.openEventStream(run, cursor)
		if err != nil {
			if session.retryObservation("event", cursor, generation, err, &reconnecting) {
				continue
			}
			session.recordFailure(fmt.Errorf("open event stream for run %s: %w", run.ID(), err))
			return
		}
		if reconnecting {
			if err = session.publishActivity(fmt.Sprintf(
				"event stream reconnected after sequence %d (replay limit %d)", cursor, session.config.ReplayLimit,
			)); err != nil {
				releaseErr := session.releaseEventStream(stream)
				session.recordFailure(errors.Join(
					fmt.Errorf("publish event reconnection: %w", err),
					releaseErr,
				))
				return
			}
			reconnecting = false
		}
		terminal, nextCursor, err := session.consumeEventStream(run, cursor, stream)
		closeErr := session.releaseEventStream(stream)
		if closeErr != nil && err == nil {
			err = fmt.Errorf("close event stream for run %s: %w", run.ID(), closeErr)
		}
		cursor = nextCursor
		if terminal {
			session.clearActiveRun(run)
			return
		}
		if err == nil {
			continue
		}
		if !session.canRetryObservation(err) {
			session.recordFailure(fmt.Errorf("observe events for run %s after sequence %d: %w", run.ID(), cursor, err))
			return
		}
		if !session.retryObservation("event", cursor, generation, err, &reconnecting) {
			return
		}
	}
}

func (session *Session) openEventStream(run client.RunRef, after uint64) (client.EventStream, uint64, error) {
	cursor, err := client.NewCursor(run, after)
	if err != nil {
		return nil, 0, err
	}
	clientSession, generation := session.currentClientGeneration()
	options, err := client.NewEventStreamOptions(
		session.config.ReplayLimit,
		true,
		clientSession.Connection().Limits(),
	)
	if err != nil {
		return nil, generation, err
	}
	stream, err := clientSession.Events(session.context(), cursor, options)
	if err != nil {
		return nil, generation, err
	}
	if stream == nil {
		return nil, generation, errors.New("client returned a nil event stream")
	}
	session.streamMutex.Lock()
	session.eventStream = stream
	session.streamMutex.Unlock()
	return stream, generation, nil
}

func (session *Session) consumeEventStream(
	run client.RunRef,
	after uint64,
	stream client.EventStream,
) (bool, uint64, error) {
	cursor := after
	for {
		frame, err := stream.Next(session.context())
		if err != nil {
			return false, cursor, err
		}
		terminal, pageComplete, nextCursor, handleErr := session.handleEventFrame(run, cursor, frame)
		if handleErr != nil {
			return false, cursor, handleErr
		}
		cursor = nextCursor
		if terminal || pageComplete {
			return terminal, cursor, nil
		}
	}
}

func (session *Session) handleEventFrame(
	run client.RunRef,
	cursor uint64,
	frame client.EventFrame,
) (bool, bool, uint64, error) {
	switch frame.Kind() {
	case client.EventFrameEvent:
		event, available := frame.Event()
		if !available {
			return false, false, cursor, errors.New("event frame has no event payload")
		}
		nextCursor, err := session.handleEvent(run, cursor, event)
		return runTerminal(event.Kind()), false, nextCursor, err
	case client.EventFrameControl:
		control, available := frame.Control()
		if !available {
			return false, false, cursor, errors.New("event control frame has no control payload")
		}
		if control.LastDeliveredSequence() != cursor {
			return false, false, cursor, fmt.Errorf(
				"event control acknowledged sequence %d, want %d",
				control.LastDeliveredSequence(), cursor,
			)
		}
		return false, control.HasMore(), cursor, nil
	case client.EventFrameKind(""):
		return false, false, cursor, errors.New("event stream returned an empty frame")
	default:
		return false, false, cursor, fmt.Errorf("event stream returned unsupported frame kind %q", frame.Kind())
	}
}

func (session *Session) handleEvent(
	run client.RunRef,
	cursor uint64,
	event client.Event,
) (uint64, error) {
	if event.Run().ID() != run.ID() || event.Sequence() != cursor+1 {
		return cursor, fmt.Errorf(
			"event sequence is not contiguous: expected %s/%d, received %s/%d",
			run.ID(), cursor+1, event.Run().ID(), event.Sequence(),
		)
	}
	if err := session.publishActivity(eventSummary(event)); err != nil {
		return cursor, err
	}
	cursor = event.Sequence()
	session.stateMutex.Lock()
	if session.hasActiveRun && session.activeRun.ID() == run.ID() {
		session.eventCursor = cursor
	}
	session.stateMutex.Unlock()
	return cursor, nil
}

func (session *Session) observeInteractions() {
	for {
		stream, generation, err := session.openInteractionStream()
		if err != nil {
			reconnecting := false
			if session.retryObservation("interaction", 0, generation, err, &reconnecting) {
				continue
			}
			session.recordFailure(fmt.Errorf("open interaction stream: %w", err))
			return
		}
		err = session.consumeInteractionStream(stream)
		closeErr := session.releaseInteractionStream(stream)
		if closeErr != nil && err == nil {
			err = fmt.Errorf("close interaction stream: %w", closeErr)
		}
		if err != nil && !session.canRetryObservation(err) {
			session.recordFailure(fmt.Errorf("observe interactions: %w", err))
			return
		}
		reconnecting := false
		if !session.retryObservation("interaction", 0, generation, err, &reconnecting) {
			return
		}
	}
}

func (session *Session) openInteractionStream() (client.InteractionStream, uint64, error) {
	clientSession, generation := session.currentClientGeneration()
	stream, err := clientSession.Interactions(
		session.context(),
		client.NewInteractionStreamOptions(true),
	)
	if err != nil {
		return nil, generation, err
	}
	if stream == nil {
		return nil, generation, errors.New("client returned a nil interaction stream")
	}
	session.streamMutex.Lock()
	session.interactionStream = stream
	session.streamMutex.Unlock()
	return stream, generation, nil
}

func (session *Session) consumeInteractionStream(stream client.InteractionStream) error {
	wantSnapshot := true
	for {
		frame, err := stream.Next(session.context())
		if err != nil {
			return err
		}
		if err = session.handleInteractionFrame(frame, &wantSnapshot); err != nil {
			return err
		}
	}
}

func (session *Session) handleInteractionFrame(
	frame client.InteractionFrame,
	wantSnapshot *bool,
) error {
	switch frame.Kind() {
	case client.InteractionFrameUpdate:
		update, available := frame.Update()
		if !available {
			return errors.New("interaction frame has no update payload")
		}
		return session.handleInteractionUpdate(update, wantSnapshot)
	case client.InteractionFrameControl:
		if *wantSnapshot {
			return errors.New("interaction stream control arrived before its snapshot")
		}
		control, available := frame.Control()
		if !available {
			return errors.New("interaction frame has no control payload")
		}
		if control.PageLastRevision() != session.currentInteractionRevision() {
			return fmt.Errorf(
				"interaction control revision %d does not match merged revision %d",
				control.PageLastRevision(), session.currentInteractionRevision(),
			)
		}
		return nil
	case client.InteractionFrameKind(""):
		return errors.New("interaction stream returned an empty frame")
	default:
		return fmt.Errorf("interaction stream returned unsupported frame kind %q", frame.Kind())
	}
}

func (session *Session) handleInteractionUpdate(
	update client.InteractionUpdate,
	wantSnapshot *bool,
) error {
	if *wantSnapshot && update.Kind() != client.InteractionSnapshot {
		return errors.New("interaction stream did not begin with a complete snapshot")
	}
	if !*wantSnapshot && update.Kind() == client.InteractionSnapshot {
		return errors.New("interaction stream returned a second snapshot")
	}
	*wantSnapshot = false
	changed, err := session.mergeInteraction(update)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return session.publishCurrentInteraction()
}

func (session *Session) mergeInteraction(update client.InteractionUpdate) (bool, error) {
	session.stateMutex.Lock()
	defer session.stateMutex.Unlock()
	previous, hadPrevious := selectInteraction(session.pending, session.activeRun, session.hasActiveRun)
	switch update.Kind() {
	case client.InteractionSnapshot:
		values, available := update.Snapshot()
		if !available {
			return false, errors.New("interaction snapshot has no snapshot payload")
		}
		if update.Revision() < session.interactionRevision {
			return false, fmt.Errorf(
				"interaction snapshot revision moved backwards from %d to %d",
				session.interactionRevision, update.Revision(),
			)
		}
		next := make(map[interactionKey]client.PendingInteraction, len(values))
		for _, value := range values {
			next[interactionKey{run: value.Run().ID(), id: value.ID()}] = value
		}
		session.pending = next
	case client.InteractionOpened, client.InteractionClosed:
		if update.Revision() != session.interactionRevision+1 {
			return false, fmt.Errorf(
				"interaction revision is not contiguous: expected %d, received %d",
				session.interactionRevision+1, update.Revision(),
			)
		}
		item, available := update.Item()
		if !available {
			return false, errors.New("interaction change has no item payload")
		}
		key := interactionKey{run: item.Run().ID(), id: item.ID()}
		if update.Kind() == client.InteractionOpened {
			if _, exists := session.pending[key]; exists {
				return false, fmt.Errorf("interaction %s/%s opened twice", key.run, key.id)
			}
			session.pending[key] = item
		} else {
			if _, exists := session.pending[key]; !exists {
				return false, fmt.Errorf("interaction %s/%s closed before opening", key.run, key.id)
			}
			delete(session.pending, key)
		}
	default:
		return false, fmt.Errorf("unsupported interaction update kind %q", update.Kind())
	}
	session.interactionRevision = update.Revision()
	current, hasCurrent := selectInteraction(session.pending, session.activeRun, session.hasActiveRun)
	return !sameInteraction(previous, hadPrevious, current, hasCurrent), nil
}

func (session *Session) publishCurrentInteraction() error {
	pending, found := session.currentInteraction()
	if !found {
		return session.publishActivity("interaction resolved")
	}
	return session.publishActivity("interaction: " + pending.Prompt())
}

func (session *Session) currentInteraction() (client.PendingInteraction, bool) {
	session.stateMutex.Lock()
	defer session.stateMutex.Unlock()
	return selectInteraction(session.pending, session.activeRun, session.hasActiveRun)
}

func selectInteraction(
	values map[interactionKey]client.PendingInteraction,
	activeRun client.RunRef,
	hasActiveRun bool,
) (client.PendingInteraction, bool) {
	keys := make([]interactionKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(left, right interactionKey) int {
		leftActive := hasActiveRun && left.run == activeRun.ID()
		rightActive := hasActiveRun && right.run == activeRun.ID()
		if leftActive != rightActive {
			if leftActive {
				return -1
			}
			return 1
		}
		if comparison := strings.Compare(left.run, right.run); comparison != 0 {
			return comparison
		}
		return strings.Compare(left.id, right.id)
	})
	if len(keys) == 0 {
		return client.PendingInteraction{}, false
	}
	return values[keys[0]], true
}

func sameInteraction(
	left client.PendingInteraction,
	hasLeft bool,
	right client.PendingInteraction,
	hasRight bool,
) bool {
	if hasLeft != hasRight {
		return false
	}
	if !hasLeft {
		return true
	}
	return left.Run().ID() == right.Run().ID() && left.ID() == right.ID() &&
		left.Kind() == right.Kind() && left.Prompt() == right.Prompt()
}

func (session *Session) currentInteractionRevision() uint64 {
	session.stateMutex.Lock()
	defer session.stateMutex.Unlock()
	return session.interactionRevision
}

func (session *Session) currentClient() client.Session {
	current, _ := session.currentClientGeneration()
	return current
}

func (session *Session) currentClientGeneration() (client.Session, uint64) {
	session.clientMutex.RLock()
	defer session.clientMutex.RUnlock()
	return session.clientSession, session.clientGeneration
}

func (session *Session) context() context.Context {
	return closeContext{done: session.closed}
}

func (session *Session) newOperationID() (client.OperationID, error) {
	value, err := session.nextIdentifier()
	if err != nil {
		return client.OperationID{}, fmt.Errorf("create operation ID: %w", err)
	}
	return client.NewOperationID(value)
}

func (session *Session) nextIdentifier() (string, error) {
	session.identifierMutex.Lock()
	defer session.identifierMutex.Unlock()
	value, err := session.identifiers.Next()
	if err != nil {
		return "", err
	}
	if _, err = client.NewOperationID(value); err != nil {
		return "", fmt.Errorf("identifier source returned an invalid value: %w", err)
	}
	return value, nil
}

func (session *Session) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	operationContext, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(session.context(), cancel)
	return operationContext, func() {
		stop()
		cancel()
	}
}

func (session *Session) clearActiveRun(run client.RunRef) {
	session.stateMutex.Lock()
	defer session.stateMutex.Unlock()
	if session.hasActiveRun && session.activeRun.ID() == run.ID() {
		session.activeRun = client.RunRef{}
		session.hasActiveRun = false
		session.eventCursor = 0
	}
}

func (session *Session) canRetryObservation(err error) bool {
	if err == nil {
		return false
	}
	select {
	case <-session.closed:
		return false
	default:
	}
	gap, isGap := errors.AsType[*client.CursorGapError](err)
	if isGap && gap != nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	if errors.Is(err, client.ErrClosed) {
		return true
	}
	if status, ok := errors.AsType[*client.StatusError](err); ok && status != nil &&
		status.Code() == client.ErrorUnauthenticated {
		return true
	}
	var failure client.StatusFailure
	if errors.As(err, &failure) && failure.Retryable() {
		return true
	}
	return false
}

func (session *Session) waitToReconnect() bool {
	if session.config.ReconnectDelay == 0 {
		select {
		case <-session.closed:
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(session.config.ReconnectDelay)
	defer timer.Stop()
	select {
	case <-session.closed:
		return false
	case <-timer.C:
		return true
	}
}

func (session *Session) startWorker(work func()) {
	session.workersMutex.Lock()
	defer session.workersMutex.Unlock()
	if session.workersClosed {
		return
	}
	session.workers.Go(work)
}

func (session *Session) releaseEventStream(stream client.EventStream) error {
	session.streamMutex.Lock()
	if session.eventStream == stream {
		session.eventStream = nil
	}
	session.streamMutex.Unlock()
	return stream.Close()
}

func (session *Session) releaseInteractionStream(stream client.InteractionStream) error {
	session.streamMutex.Lock()
	if session.interactionStream == stream {
		session.interactionStream = nil
	}
	session.streamMutex.Unlock()
	return stream.Close()
}

func (session *Session) closeStreams() error {
	session.streamMutex.Lock()
	eventStream := session.eventStream
	interactionStream := session.interactionStream
	session.eventStream = nil
	session.interactionStream = nil
	session.streamMutex.Unlock()
	var eventErr, interactionErr error
	if eventStream != nil {
		eventErr = eventStream.Close()
	}
	if interactionStream != nil {
		interactionErr = interactionStream.Close()
	}
	return errors.Join(eventErr, interactionErr)
}

// Close stops observation, closes the negotiated client session, and waits for
// adapter workers. It is safe for concurrent and repeated lifecycle cleanup.
func (session *Session) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("close TUI session: context must not be nil")
	}
	session.closeOnce.Do(func() { go session.close() })
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-session.closeDone:
		return session.closeResult
	}
}

func (session *Session) close() {
	defer close(session.closeDone)
	close(session.closed)
	session.initializeOnce.Do(func() {
		session.clientMutex.Lock()
		session.initializeErr = client.ErrClosed
		session.clientMutex.Unlock()
		close(session.initializeDone)
	})
	session.workersMutex.Lock()
	session.workersClosed = true
	session.workersMutex.Unlock()
	streamErr := session.closeStreams()
	<-session.initializeDone
	clientSession := session.currentClient()
	var clientErr error
	if clientSession != nil {
		clientErr = clientSession.Close()
	}
	session.workers.Wait()
	session.closeResult = errors.Join(streamErr, clientErr)
}

func (session *Session) recordFailure(err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, client.ErrClosed) {
		return
	}
	session.failureMutex.Lock()
	if session.failure != nil {
		session.failureMutex.Unlock()
		return
	}
	session.failure = err
	session.failureMutex.Unlock()
	select {
	case <-session.closed:
	case session.updates <- delivery{err: err}:
	}
}

func (session *Session) deliveredFailure() error {
	session.failureMutex.RLock()
	defer session.failureMutex.RUnlock()
	if session.failureDelivered {
		return session.failure
	}
	return nil
}

func (session *Session) markFailureDelivered() {
	session.failureMutex.Lock()
	session.failureDelivered = true
	session.failureMutex.Unlock()
}

func appendBoundedHistory(history []agenttui.Text, prompt agenttui.Text) []agenttui.Text {
	result := append(slices.Clone(history), prompt)
	if len(result) > agenttui.MaximumPromptHistoryItems {
		result = result[len(result)-agenttui.MaximumPromptHistoryItems:]
	}
	return result
}

func commandResult(value string) (agenttui.CommandResult, error) {
	text, err := presentationText(value)
	if err != nil {
		return agenttui.CommandResult{}, err
	}
	return agenttui.NewCommandResult(text, nil)
}

func eventSummary(event client.Event) string {
	detail := event.Detail()
	if value, available := detail.Text(); available {
		return value
	}
	if value, available := detail.Status(); available {
		return string(event.Kind()) + ": " + value
	}
	if failure, available := detail.ModelFailure(); available {
		return string(event.Kind()) + ": " + failure.Message()
	}
	if _, name, available := detail.ToolStart(); available {
		return string(event.Kind()) + ": " + name
	}
	if _, message, available := detail.ToolProgress(); available {
		return string(event.Kind()) + ": " + message
	}
	if terminal, available := detail.ToolTerminal(); available {
		return string(event.Kind()) + ": " + terminal.Name() + " " + terminal.Problem()
	}
	if _, kind, available := detail.InteractionStart(); available {
		return string(event.Kind()) + ": " + kind
	}
	if _, status, available := detail.InteractionTerminal(); available {
		return string(event.Kind()) + ": " + status
	}
	return string(event.Kind())
}

func runTerminal(kind client.EventKind) bool {
	return kind == client.EventRunCompleted || kind == client.EventRunFailed || kind == client.EventRunCancelled
}

func presentationText(value string) (agenttui.Text, error) {
	value = strings.ToValidUTF8(value, "�")
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return -1
		}
		return character
	}, value)
	if len(value) > agenttui.MaximumTextBytes {
		value = value[:agenttui.MaximumTextBytes]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return agenttui.NewText(value)
}

var _ agenttui.Session = (*Session)(nil)
