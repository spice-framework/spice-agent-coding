package daemon

import (
	"context"
	"errors"
	"math"
	"slices"
)

type reconnectWaiter struct {
	fencing bool
}

type reconnectStep struct {
	streamCancels []context.CancelCauseFunc
	oldCancel     context.CancelFunc
	next          Session
	wait          <-chan struct{}
	err           error
	done          bool
}

// Reconnect performs an exact compare-and-swap to the next ownership epoch.
// It preserves the original blocking API; callers needing cancellation should
// use ReconnectContext.
func (store *SessionStore) Reconnect(clientID string, expected uint64) (Session, error) {
	return store.ReconnectContext(context.Background(), clientID, expected)
}

// ReconnectContext registers a priority reconnect intent, drains the current
// mutation commit, cancels and joins every old stream lease, and advances the
// epoch only after all fences are closed. Cancellation before the epoch CAS
// removes the intent and lets the old epoch's FIFO continue.
func (store *SessionStore) ReconnectContext(ctx context.Context, clientID string, expected uint64) (Session, error) {
	if store == nil {
		return Session{}, ErrSessionStoreClosed
	}
	if ctx == nil {
		return Session{}, errors.New("reconnect context is nil")
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if err := boundedToken("client ID", clientID); err != nil {
		return Session{}, err
	}
	state, waiter, err := store.registerReconnect(clientID, expected)
	if err != nil {
		return Session{}, err
	}

	for {
		if err = ctx.Err(); err != nil {
			store.mu.Lock()
			store.removeReconnectLocked(state, waiter)
			store.mu.Unlock()
			return Session{}, err
		}
		store.mu.Lock()
		step := store.reconnectStepLocked(clientID, expected, state, waiter)
		store.mu.Unlock()

		if len(step.streamCancels) > 0 {
			for _, cancel := range step.streamCancels {
				cancel(ErrStaleSession)
			}
			continue
		}
		if step.oldCancel != nil {
			step.oldCancel()
		}
		if step.done {
			return step.next, step.err
		}
		waitForSessionChange(ctx, store.root, step.wait)
	}
}

func (store *SessionStore) registerReconnect(
	clientID string,
	expected uint64,
) (*sessionState, *reconnectWaiter, error) {
	waiter := &reconnectWaiter{}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, err := store.sessionLocked(clientID, expected)
	if err != nil {
		return nil, nil, err
	}
	if expected == math.MaxUint64 {
		return nil, nil, errors.New("session ownership epoch overflow")
	}
	if store.gateWaitersLocked(state) >= maximumSessionGateWaitersPerClient {
		return nil, nil, newSessionGateCapacity("session gate waiters", maximumSessionGateWaitersPerClient)
	}
	state.reconnectWaiters = append(state.reconnectWaiters, waiter)
	store.signalLocked(state)
	return state, waiter, nil
}

func (store *SessionStore) reconnectStepLocked(
	clientID string,
	expected uint64,
	state *sessionState,
	waiter *reconnectWaiter,
) reconnectStep {
	if store.closedLocked() {
		store.removeReconnectLocked(state, waiter)
		return reconnectStep{err: ErrSessionStoreClosed, done: true}
	}
	if state.epoch != expected {
		store.removeReconnectLocked(state, waiter)
		return reconnectStep{err: staleSession(clientID, state.epoch, expected), done: true}
	}
	if state.reconnectWaiters[0] != waiter || state.activeMutation != nil {
		return reconnectStep{wait: state.changed}
	}
	if !waiter.fencing {
		waiter.fencing = true
		cancels := make([]context.CancelCauseFunc, 0, len(state.streams))
		for stream := range state.streams {
			cancels = append(cancels, stream.cancel)
		}
		if len(cancels) != 0 {
			return reconnectStep{streamCancels: cancels}
		}
	}
	if len(state.streams) != 0 {
		return reconnectStep{wait: state.changed}
	}
	return store.advanceReconnectLocked(clientID, state, waiter)
}

func (store *SessionStore) advanceReconnectLocked(
	clientID string,
	state *sessionState,
	waiter *reconnectWaiter,
) reconnectStep {
	newContext, newCancel := context.WithCancel(store.root)
	if newContext.Err() != nil {
		newCancel()
		store.removeReconnectLocked(state, waiter)
		return reconnectStep{err: ErrSessionStoreClosed, done: true}
	}
	oldCancel := state.cancel
	state.epoch++
	state.ctx = newContext
	state.cancel = newCancel
	state.reconnectWaiters = state.reconnectWaiters[1:]
	store.signalLocked(state)
	return reconnectStep{
		oldCancel: oldCancel,
		next:      Session{clientID: clientID, epoch: state.epoch, ctx: newContext},
		done:      true,
	}
}

func (store *SessionStore) removeReconnectLocked(state *sessionState, waiter *reconnectWaiter) {
	if state == nil || waiter == nil {
		return
	}
	for index, current := range state.reconnectWaiters {
		if current == waiter {
			state.reconnectWaiters = slices.Delete(state.reconnectWaiters, index, index+1)
			store.signalLocked(state)
			break
		}
	}
	store.maybeDrainedLocked()
}
