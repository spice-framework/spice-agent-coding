package daemon

import (
	"context"
	"errors"
	"slices"
	"sync"
)

const (
	maximumSessionGateWaitersPerClient = 64
	maximumSessionStreamsPerClient     = 256
)

// mutationWaiter is deliberately non-zero-sized so pointer identity remains a
// reliable FIFO token under the Go memory model.
type mutationWaiter struct{ _ byte }

// streamWaiter is a bounded reconnect-blocked acquisition token. Streams do
// not require FIFO admission, but their pending acquisitions remain tracked so
// orderly shutdown cannot report a false drain.
type streamWaiter struct{ _ byte }

// AcquireMutationCommit obtains the exclusive commit boundary for one client
// epoch. Contended acquisitions are bounded and FIFO. A registered reconnect
// intent takes priority so old queued mutations cannot commit after a claimant
// begins fencing. Close the returned lease immediately after commit certainty
// is known; cancellation after acquisition never releases it implicitly.
func (store *SessionStore) AcquireMutationCommit(
	ctx context.Context,
	clientID string,
	epoch uint64,
) (*MutationCommitLease, error) {
	if store == nil {
		return nil, ErrSessionStoreClosed
	}
	if ctx == nil {
		return nil, errors.New("mutation commit context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := boundedToken("client ID", clientID); err != nil {
		return nil, err
	}

	var waiter *mutationWaiter
	for {
		if err := ctx.Err(); err != nil {
			store.mu.Lock()
			store.removeMutationWaiterLocked(store.sessions[clientID], waiter)
			store.mu.Unlock()
			return nil, err
		}
		store.mu.Lock()
		state, err := store.sessionLocked(clientID, epoch)
		if err != nil {
			store.removeMutationWaiterLocked(state, waiter)
			store.mu.Unlock()
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			store.removeMutationWaiterLocked(state, waiter)
			store.mu.Unlock()
			return nil, err
		}
		lease, currentWaiter, acquired, acquireErr := store.tryAcquireMutationLocked(state, waiter)
		waiter = currentWaiter
		if acquired || acquireErr != nil {
			store.mu.Unlock()
			return lease, acquireErr
		}
		changed := state.changed
		store.mu.Unlock()
		waitForSessionChange(ctx, store.root, changed)
	}
}

func (store *SessionStore) tryAcquireMutationLocked(
	state *sessionState,
	waiter *mutationWaiter,
) (*MutationCommitLease, *mutationWaiter, bool, error) {
	if waiter == nil && state.activeMutation == nil && len(state.mutationWaiters) == 0 && len(state.reconnectWaiters) == 0 {
		return store.grantMutationLocked(state), nil, true, nil
	}
	if waiter == nil {
		if store.gateWaitersLocked(state) >= maximumSessionGateWaitersPerClient {
			return nil, nil, false, newSessionGateCapacity("session gate waiters", maximumSessionGateWaitersPerClient)
		}
		waiter = &mutationWaiter{}
		state.mutationWaiters = append(state.mutationWaiters, waiter)
	}
	if len(state.reconnectWaiters) != 0 || state.activeMutation != nil || state.mutationWaiters[0] != waiter {
		return nil, waiter, false, nil
	}
	state.mutationWaiters = state.mutationWaiters[1:]
	store.signalLocked(state)
	return store.grantMutationLocked(state), waiter, true, nil
}

func (store *SessionStore) grantMutationLocked(state *sessionState) *MutationCommitLease {
	lease := &MutationCommitLease{store: store, state: state}
	state.activeMutation = lease
	return lease
}

func waitForSessionChange(ctx, root context.Context, changed <-chan struct{}) {
	select {
	case <-ctx.Done():
	case <-root.Done():
	case <-changed:
	}
}

// MutationCommitLease is an opaque exclusive commit-boundary lease. Close is
// nonblocking, idempotent, and safe for concurrent use.
type MutationCommitLease struct {
	store *SessionStore
	state *sessionState
	once  sync.Once
}

// Close releases the client's commit boundary.
func (lease *MutationCommitLease) Close() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		if lease.store == nil {
			return
		}
		lease.store.mu.Lock()
		if lease.state != nil && lease.state.activeMutation == lease {
			lease.state.activeMutation = nil
			lease.store.signalLocked(lease.state)
		}
		lease.store.maybeDrainedLocked()
		lease.store.mu.Unlock()
	})
}

// AcquireStream registers one old-frame fence for a client epoch. The returned
// Context is canceled by its caller context, reconnect, daemon-root cancellation,
// or store closure. A stream handler must stop and join every sender before it
// closes the lease; a successful ReconnectContext cannot advance or return
// while any old stream lease remains registered.
func (store *SessionStore) AcquireStream(ctx context.Context, clientID string, epoch uint64) (*StreamLease, error) {
	return store.acquireStream(ctx, clientID, epoch, maximumSessionStreamsPerClient)
}

func (store *SessionStore) acquireStream(
	ctx context.Context,
	clientID string,
	epoch uint64,
	maximum int,
) (*StreamLease, error) {
	if store == nil {
		return nil, ErrSessionStoreClosed
	}
	if maximum < 1 || maximum > maximumSessionStreamsPerClient {
		return nil, errors.New("stream lease limit is invalid")
	}
	if ctx == nil {
		return nil, errors.New("stream context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := boundedToken("client ID", clientID); err != nil {
		return nil, err
	}

	var waiter *streamWaiter
	for {
		if err := ctx.Err(); err != nil {
			store.mu.Lock()
			store.removeStreamWaiterLocked(store.sessions[clientID], waiter)
			store.mu.Unlock()
			return nil, err
		}
		store.mu.Lock()
		state, err := store.sessionLocked(clientID, epoch)
		if err != nil {
			store.removeStreamWaiterLocked(state, waiter)
			store.mu.Unlock()
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			store.removeStreamWaiterLocked(state, waiter)
			store.mu.Unlock()
			return nil, err
		}
		if len(state.reconnectWaiters) == 0 {
			store.removeStreamWaiterLocked(state, waiter)
			if len(state.streams) >= maximum {
				store.mu.Unlock()
				return nil, newSessionGateCapacity("stream leases", maximum)
			}
			streamContext, cancel := context.WithCancelCause(state.ctx)
			lease := &StreamLease{store: store, state: state, ctx: streamContext, cancel: cancel}
			state.streams[lease] = struct{}{}
			store.mu.Unlock()

			stop := context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })
			lease.setCallerStop(stop)
			return lease, nil
		}
		if waiter == nil {
			if store.gateWaitersLocked(state) >= maximumSessionGateWaitersPerClient {
				store.mu.Unlock()
				return nil, newSessionGateCapacity(
					"session gate waiters",
					maximumSessionGateWaitersPerClient,
				)
			}
			waiter = &streamWaiter{}
			state.streamWaiters = append(state.streamWaiters, waiter)
		}
		changed := state.changed
		store.mu.Unlock()
		waitForSessionChange(ctx, store.root, changed)
	}
}

// StreamLease is an opaque reconnect fence. Context supplies the cooperative
// cancellation signal; Close acknowledges that all stream senders have joined.
// Close is nonblocking, idempotent, and safe for concurrent use.
type StreamLease struct {
	store  *SessionStore
	state  *sessionState
	ctx    context.Context //nolint:containedctx // this lease is the public owner of its stream lifetime.
	cancel context.CancelCauseFunc

	mu         sync.Mutex
	callerStop func() bool
	closed     bool
}

// Context returns the stream's cooperative lifetime context.
func (lease *StreamLease) Context() context.Context {
	if lease == nil || lease.ctx == nil {
		return context.Background()
	}
	return lease.ctx
}

// Close acknowledges that the stream's senders have joined and releases its
// reconnect fence.
func (lease *StreamLease) Close() {
	if lease == nil {
		return
	}
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		return
	}
	lease.closed = true
	stop := lease.callerStop
	lease.mu.Unlock()

	if stop != nil {
		stop()
	}
	if lease.cancel != nil {
		lease.cancel(context.Canceled)
	}
	if lease.store == nil {
		return
	}
	lease.store.mu.Lock()
	if lease.state != nil {
		delete(lease.state.streams, lease)
		lease.store.signalLocked(lease.state)
	}
	lease.store.maybeDrainedLocked()
	lease.store.mu.Unlock()
}

func (lease *StreamLease) setCallerStop(stop func() bool) {
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		stop()
		return
	}
	lease.callerStop = stop
	lease.mu.Unlock()
}

func (store *SessionStore) gateWaitersLocked(state *sessionState) int {
	return len(state.mutationWaiters) + len(state.reconnectWaiters) + len(state.streamWaiters)
}

func (store *SessionStore) removeStreamWaiterLocked(state *sessionState, waiter *streamWaiter) {
	if state == nil || waiter == nil {
		return
	}
	for index, current := range state.streamWaiters {
		if current == waiter {
			state.streamWaiters = slices.Delete(state.streamWaiters, index, index+1)
			store.signalLocked(state)
			break
		}
	}
	store.maybeDrainedLocked()
}

func (store *SessionStore) removeMutationWaiterLocked(state *sessionState, waiter *mutationWaiter) {
	if state == nil || waiter == nil {
		return
	}
	for index, current := range state.mutationWaiters {
		if current == waiter {
			state.mutationWaiters = slices.Delete(state.mutationWaiters, index, index+1)
			store.signalLocked(state)
			break
		}
	}
	store.maybeDrainedLocked()
}
