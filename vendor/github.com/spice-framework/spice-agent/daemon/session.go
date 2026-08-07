package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
)

const maximumSessions = 4096

// Session is one immutable client ownership epoch.
type Session struct {
	clientID string
	epoch    uint64
	ctx      context.Context //nolint:containedctx // an immutable session owns its epoch lifetime.
}

// ClientID returns the stable cryptographic identity preserved by reconnect.
func (session Session) ClientID() string { return session.clientID }

// Epoch returns the current ownership generation.
func (session Session) Epoch() uint64 { return session.epoch }

// Context returns the daemon-root-owned epoch context.
func (session Session) Context() context.Context { return session.ctx }

type sessionState struct {
	epoch            uint64
	ctx              context.Context //nolint:containedctx // state owns the current fencing lifetime.
	cancel           context.CancelFunc
	changed          chan struct{}
	activeMutation   *MutationCommitLease
	mutationWaiters  []*mutationWaiter
	reconnectWaiters []*reconnectWaiter
	streamWaiters    []*streamWaiter
	streams          map[*StreamLease]struct{}
}

// SessionStore assigns stable cryptographic client identities and fences stale
// owners. Each client has an independent, bounded FIFO mutation-commit gate.
// Reconnect intents take priority over queued commits, drain an active commit,
// cancel and join old streams, and only then advance the ownership epoch. All
// epoch contexts derive from the caller-owned daemon root.
type SessionStore struct {
	mu       sync.Mutex
	root     context.Context //nolint:containedctx // the store derives and owns every session lifetime from this root.
	rootStop context.CancelFunc
	maximum  int
	random   io.Reader
	randomMu sync.Mutex
	sessions map[string]*sessionState
	closed   bool
	drained  chan struct{}
	drain    sync.Once
}

// NewSessionStore constructs a bounded store owned by root.
func NewSessionStore(root context.Context, maximum int) (*SessionStore, error) {
	return newSessionStore(root, maximum, rand.Reader)
}

func newSessionStore(root context.Context, maximum int, random io.Reader) (*SessionStore, error) {
	if root == nil || maximum < 1 || maximum > maximumSessions || random == nil {
		return nil, fmt.Errorf("session store requires a root context, capacity between 1 and %d, and randomness", maximumSessions)
	}
	ownedRoot, cancelRoot := context.WithCancel(root)
	store := &SessionStore{
		root: ownedRoot, rootStop: cancelRoot, maximum: maximum, random: random,
		sessions: map[string]*sessionState{}, drained: make(chan struct{}),
	}
	context.AfterFunc(ownedRoot, store.Close)
	return store, nil
}

// Fresh creates a cryptographically random stable client ID at epoch one.
func (store *SessionStore) Fresh() (Session, error) {
	if store == nil {
		return Session{}, ErrSessionStoreClosed
	}
	store.mu.Lock()
	if store.closedLocked() {
		store.mu.Unlock()
		return Session{}, ErrSessionStoreClosed
	}
	if len(store.sessions) >= store.maximum {
		store.mu.Unlock()
		return Session{}, errors.New("daemon session capacity exhausted")
	}
	store.mu.Unlock()
	for range 4 {
		raw := make([]byte, 16)
		store.randomMu.Lock()
		_, randomErr := io.ReadFull(store.random, raw)
		store.randomMu.Unlock()
		if randomErr != nil {
			return Session{}, fmt.Errorf("generate client ID: %w", randomErr)
		}
		clientID := hex.EncodeToString(raw)
		store.mu.Lock()
		if store.closedLocked() {
			store.mu.Unlock()
			return Session{}, ErrSessionStoreClosed
		}
		if len(store.sessions) >= store.maximum {
			store.mu.Unlock()
			return Session{}, errors.New("daemon session capacity exhausted")
		}
		if _, exists := store.sessions[clientID]; exists {
			store.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(store.root)
		if ctx.Err() != nil {
			cancel()
			store.mu.Unlock()
			return Session{}, ErrSessionStoreClosed
		}
		state := &sessionState{
			epoch: 1, ctx: ctx, cancel: cancel, changed: make(chan struct{}),
			streams: make(map[*StreamLease]struct{}),
		}
		store.sessions[clientID] = state
		store.mu.Unlock()
		return Session{clientID: clientID, epoch: 1, ctx: ctx}, nil
	}
	return Session{}, errors.New("generate unique client ID")
}

// Check verifies that an epoch still owns the stable client identity.
func (store *SessionStore) Check(clientID string, epoch uint64) error {
	if store == nil {
		return ErrSessionStoreClosed
	}
	if err := boundedToken("client ID", clientID); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	_, err := store.sessionLocked(clientID, epoch)
	return err
}

// Fence returns the current ownership context or rejects a stale epoch.
func (store *SessionStore) Fence(clientID string, epoch uint64) (context.Context, error) {
	if store == nil {
		return nil, ErrSessionStoreClosed
	}
	if err := boundedToken("client ID", clientID); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, err := store.sessionLocked(clientID, epoch)
	if err != nil {
		return nil, err
	}
	return state.ctx, nil
}

// Close fences every owner, cancels streams, wakes queued acquisitions, and
// rejects future session work. It is nonblocking and idempotent; Shutdown waits
// for active commit and stream leases when an orderly join is required.
func (store *SessionStore) Close() {
	if store == nil {
		return
	}
	store.mu.Lock()
	if store.closed {
		store.mu.Unlock()
		return
	}
	store.closed = true
	rootStop := store.rootStop
	cancels := make([]context.CancelFunc, 0, len(store.sessions))
	streamCancels := make([]context.CancelCauseFunc, 0)
	for _, state := range store.sessions {
		cancels = append(cancels, state.cancel)
		for stream := range state.streams {
			streamCancels = append(streamCancels, stream.cancel)
		}
		store.signalLocked(state)
	}
	store.maybeDrainedLocked()
	store.mu.Unlock()

	for _, cancel := range streamCancels {
		cancel(ErrSessionStoreClosed)
	}
	for _, cancel := range cancels {
		cancel()
	}
	if rootStop != nil {
		rootStop()
	}
}

// Shutdown closes the store and waits for every active commit/stream lease and
// queued gate claimant to release. The caller-owned context bounds the join.
func (store *SessionStore) Shutdown(ctx context.Context) error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	uninitialized := store.drained == nil
	store.mu.Unlock()
	if uninitialized {
		store.Close()
		return nil
	}
	if ctx == nil {
		return errors.New("session shutdown context is nil")
	}
	store.Close()
	select {
	case <-store.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (store *SessionStore) sessionLocked(clientID string, observed uint64) (*sessionState, error) {
	state, exists := store.sessions[clientID]
	if store.closedLocked() {
		return state, ErrSessionStoreClosed
	}
	if !exists {
		return nil, ErrStaleSession
	}
	if observed == 0 || state.epoch != observed {
		return state, staleSession(clientID, state.epoch, observed)
	}
	return state, nil
}

func (store *SessionStore) closedLocked() bool {
	return store.closed || store.root == nil || store.root.Err() != nil
}

func (store *SessionStore) signalLocked(state *sessionState) {
	if state == nil {
		return
	}
	close(state.changed)
	state.changed = make(chan struct{})
}

func (store *SessionStore) maybeDrainedLocked() {
	if !store.closed {
		return
	}
	for _, state := range store.sessions {
		if state.activeMutation != nil || len(state.mutationWaiters) != 0 || len(state.reconnectWaiters) != 0 ||
			len(state.streamWaiters) != 0 || len(state.streams) != 0 {
			return
		}
	}
	if store.drained == nil {
		return
	}
	store.drain.Do(func() { close(store.drained) })
}
