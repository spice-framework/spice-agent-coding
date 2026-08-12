package tuisession

import (
	"context"
	"errors"
	"fmt"

	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice/lifecycle"
)

// Session is the lifecycle-owned adapter implementation. Its embedded
// collaborators retain distinct ownership for connection, presentation,
// commands, observation, state, resources, and cleanup.
type Session struct {
	config      Config
	connector   client.Connector
	identifiers IdentifierSource

	*sessionConnection
	*sessionPublisher
	*sessionCommands
	*sessionEventObserver
	*sessionInteractionObserver
	*sessionState
	*sessionResources
	*sessionLifecycle
}

// NewSession creates an I/O-lazy TUI session and lifecycle cleanup. It never
// resolves an endpoint, starts a daemon, or opens a connection during
// construction.
func NewSession(
	config Config,
	connector client.Connector,
	identifiers IdentifierSource,
) (*Session, lifecycle.Cleanup, error) {
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
		sessionConnection:          &sessionConnection{initializeDone: make(chan struct{})},
		sessionPublisher:           &sessionPublisher{updates: make(chan delivery, config.UpdateCapacity)},
		sessionCommands:            &sessionCommands{},
		sessionEventObserver:       &sessionEventObserver{},
		sessionInteractionObserver: &sessionInteractionObserver{},
		sessionState:               &sessionState{pending: make(map[interactionKey]client.PendingInteraction)},
		sessionResources:           &sessionResources{},
		sessionLifecycle:           &sessionLifecycle{closed: make(chan struct{}), closeDone: make(chan struct{})},
	}
	session.sessionConnection.owner = session
	session.sessionPublisher.owner = session
	session.sessionCommands.owner = session
	session.sessionEventObserver.owner = session
	session.sessionInteractionObserver.owner = session
	session.sessionState.owner = session
	session.sessionResources.owner = session
	session.sessionLifecycle.owner = session
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
