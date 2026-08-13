package tuittest

import (
	"context"
	"errors"
	"fmt"
	"sync"

	agenttui "github.com/spice-framework/spice-agent-tui"
)

// ScriptSession is a concurrency-safe fake Session for TUI tests.
//
// Receive pops the next queued update or blocks until one is pushed / context
// ends. Perform records intents and returns the configured result or error.
type ScriptSession struct {
	mu sync.Mutex

	updates       []agenttui.SessionUpdate
	notify        chan struct{}
	performResult agenttui.CommandResult
	hasResult     bool
	performErr    error
	intents       []agenttui.Intent
	receiveCalls  int
	performCalls  int
	closed        bool
}

// NewScriptSession constructs an empty scripted session.
func NewScriptSession() *ScriptSession {
	return &ScriptSession{notify: make(chan struct{})}
}

// PushUpdate enqueues one session update for a later Receive.
func (session *ScriptSession) PushUpdate(update agenttui.SessionUpdate) error {
	if err := update.Validate(); err != nil {
		return fmt.Errorf("script session update: %w", err)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return errors.New("script session is closed")
	}
	session.updates = append(session.updates, update)
	session.signalLocked()
	return nil
}

// SetPerformResult configures subsequent Perform calls to succeed.
func (session *ScriptSession) SetPerformResult(result agenttui.CommandResult) error {
	if err := result.Validate(); err != nil {
		return fmt.Errorf("script session result: %w", err)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return errors.New("script session is closed")
	}
	session.performResult = result
	session.hasResult = true
	session.performErr = nil
	return nil
}

// SetPerformError configures Perform to fail.
func (session *ScriptSession) SetPerformError(err error) error {
	if err == nil {
		return errors.New("perform error must not be nil")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return errors.New("script session is closed")
	}
	session.performErr = err
	session.performResult = agenttui.CommandResult{}
	session.hasResult = false
	return nil
}

// Intents returns a defensive copy of performed intents in order.
func (session *ScriptSession) Intents() []agenttui.Intent {
	session.mu.Lock()
	defer session.mu.Unlock()
	return append([]agenttui.Intent(nil), session.intents...)
}

// Stats returns receive/perform call counts.
func (session *ScriptSession) Stats() (receiveCalls, performCalls int) {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.receiveCalls, session.performCalls
}

// Close unblocks receivers and rejects later work or configuration.
func (session *ScriptSession) Close() {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return
	}
	session.closed = true
	session.signalLocked()
}

// Receive implements agenttui.Session.
func (session *ScriptSession) Receive(ctx context.Context) (agenttui.SessionUpdate, error) {
	if ctx == nil {
		return agenttui.SessionUpdate{}, errors.New("receive context must not be nil")
	}
	session.mu.Lock()
	session.receiveCalls++
	session.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return agenttui.SessionUpdate{}, err
	}
	for {
		session.mu.Lock()
		if session.closed {
			session.mu.Unlock()
			return agenttui.SessionUpdate{}, errors.New("script session is closed")
		}
		if len(session.updates) > 0 {
			update := session.updates[0]
			session.updates = session.updates[1:]
			session.mu.Unlock()
			return update, nil
		}
		if session.notify == nil {
			session.notify = make(chan struct{})
		}
		wait := session.notify
		session.mu.Unlock()
		select {
		case <-ctx.Done():
			return agenttui.SessionUpdate{}, ctx.Err()
		case <-wait:
		}
	}
}

// Perform implements agenttui.Session.
func (session *ScriptSession) Perform(
	ctx context.Context,
	intent agenttui.Intent,
) (agenttui.CommandResult, error) {
	if ctx == nil {
		return agenttui.CommandResult{}, errors.New("perform context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return agenttui.CommandResult{}, err
	}
	if err := intent.Validate(); err != nil {
		return agenttui.CommandResult{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	session.performCalls++
	if session.closed {
		return agenttui.CommandResult{}, errors.New("script session is closed")
	}
	session.intents = append(session.intents, intent)
	if session.performErr != nil {
		return agenttui.CommandResult{}, session.performErr
	}
	if !session.hasResult {
		return agenttui.CommandResult{}, errors.New("script session perform result is not configured")
	}
	return session.performResult, nil
}

func (session *ScriptSession) signalLocked() {
	if session.notify == nil {
		session.notify = make(chan struct{})
	}
	close(session.notify)
	session.notify = make(chan struct{})
}

var _ agenttui.Session = (*ScriptSession)(nil)
