package grpcserver

import (
	"context"
	"errors"
	"math"
	"sync"

	"github.com/spice-framework/spice-agent/daemon"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
	"google.golang.org/protobuf/proto"
)

const (
	maximumNegotiatedSessions        = 4096
	maximumReconnectWaitersPerClient = 64
)

var (
	errNegotiatedSessionClosed      = errors.New("negotiated session registry is closed")
	errNegotiatedSessionCapacity    = errors.New("negotiated session capacity is exhausted")
	errNegotiatedSessionWaiterLimit = errors.New("negotiated reconnect waiter capacity is exhausted")
	errNegotiatedSessionInvalid     = errors.New("negotiated session is invalid")
	errNegotiatedSessionUnavailable = errors.New("negotiated session is unavailable")
)

type negotiatedCapacityError struct {
	target   error
	resource string
	limit    uint64
}

func (failure *negotiatedCapacityError) Error() string {
	if failure == nil || failure.target == nil {
		return errNegotiatedSessionCapacity.Error()
	}
	return failure.target.Error()
}

func (failure *negotiatedCapacityError) Is(target error) bool {
	return failure != nil && target == failure.target
}

// negotiatedSession is the immutable adapter view used by RPC handlers.
type negotiatedSession struct {
	session          daemon.Session
	response         *enginev1.InitializeResponse
	gate             *initializationGate
	creationAttempt  string
	reconnectAttempt string
}

type initializationGate struct {
	token   chan struct{}
	waiters int
}

// negotiatedSessionRegistry binds completed Initialize responses to the exact
// daemon ownership epochs that produced them. It does not own SessionStore.
type negotiatedSessionRegistry struct {
	root          context.Context //nolint:containedctx // caller-owned service lifetime only.
	stopRootWatch func() bool
	maximum       int

	mu       sync.Mutex
	entries  map[string]negotiatedSession
	closed   bool
	closedCh chan struct{}
	closeOne sync.Once

	activeTransactions uint32
	freshReservations  int
	pendingAttempts    int
	attempts           map[string]*initializationAttemptRecord
}

func newNegotiatedSessionRegistry(root context.Context, maximum int) (*negotiatedSessionRegistry, error) {
	if root == nil || maximum < 1 || maximum > maximumNegotiatedSessions {
		return nil, errNegotiatedSessionInvalid
	}
	if err := root.Err(); err != nil {
		return nil, errNegotiatedSessionClosed
	}
	registry := &negotiatedSessionRegistry{
		root: root, maximum: maximum,
		entries:  make(map[string]negotiatedSession),
		attempts: make(map[string]*initializationAttemptRecord), closedCh: make(chan struct{}),
	}
	registry.mu.Lock()
	registry.stopRootWatch = context.AfterFunc(root, registry.close)
	registry.mu.Unlock()
	return registry, nil
}

// installFresh installs exactly one epoch-one session. Duplicate identities are
// deliberately indistinguishable from other unavailable ownership claims.
func (registry *negotiatedSessionRegistry) installFresh(
	session daemon.Session,
	response *enginev1.InitializeResponse,
) error {
	if registry == nil {
		return errNegotiatedSessionClosed
	}
	validated, err := validateNegotiatedSession(session, response)
	if err != nil || session.Epoch() != 1 {
		return errNegotiatedSessionInvalid
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closedLocked() {
		return errNegotiatedSessionClosed
	}
	if _, exists := registry.entries[session.ClientID()]; exists {
		return errNegotiatedSessionUnavailable
	}
	if len(registry.entries)+registry.freshReservations >= registry.maximum {
		return &negotiatedCapacityError{
			target: errNegotiatedSessionCapacity, resource: "negotiated sessions",
			limit: uint64(registry.maximum), // #nosec G115 -- construction bounds maximum to 1..4096.
		}
	}
	entry, err := registry.newEntryLocked(session, validated)
	if err != nil {
		return err
	}
	registry.entries[session.ClientID()] = entry
	return nil
}

// initializeFresh reserves registry capacity before SessionStore allocation,
// then commits both views as one server initialization transaction. The
// reservation prevents an otherwise successful Fresh call from being orphaned
// by negotiated-registry capacity contention.
func (registry *negotiatedSessionRegistry) initializeFresh(
	ctx context.Context,
	operation func() (daemon.Session, *enginev1.InitializeResponse, error),
) (*enginev1.InitializeResponse, error) {
	return registry.initializeFreshAttempt(ctx, nil, operation)
}

func (registry *negotiatedSessionRegistry) initializeFreshAttempt(
	ctx context.Context,
	attempt *initializationAttemptLease,
	operation func() (daemon.Session, *enginev1.InitializeResponse, error),
) (*enginev1.InitializeResponse, error) {
	if registry == nil || operation == nil {
		return nil, errNegotiatedSessionClosed
	}
	if ctx == nil {
		return nil, errNegotiatedSessionInvalid
	}
	if err := registry.beginFreshTransaction(ctx); err != nil {
		return nil, err
	}
	defer registry.finishTransaction(true, nil, true)
	session, response, err := operation()
	if err != nil {
		return nil, err
	}
	validated, err := validateNegotiatedSession(session, response)
	if err != nil || session.Epoch() != 1 {
		return nil, errNegotiatedSessionInvalid
	}
	if attempt != nil && string(validated.GetInitializationAttemptId()) != attempt.id {
		return nil, errNegotiatedSessionInvalid
	}
	registry.mu.Lock()
	if err = registry.validateAttemptLeaseLocked(attempt); err != nil {
		registry.mu.Unlock()
		return nil, err
	}
	if _, exists := registry.entries[session.ClientID()]; exists {
		registry.mu.Unlock()
		return nil, errNegotiatedSessionUnavailable
	}
	registry.entries[session.ClientID()] = negotiatedSession{
		session: session, response: validated, gate: newInitializationGate(),
	}
	if attempt != nil {
		if err = attempt.commitLocked(validated, false); err != nil {
			delete(registry.entries, session.ClientID())
			registry.mu.Unlock()
			return nil, err
		}
	}
	terminalErr := registry.transactionTerminalError(ctx, session)
	registry.mu.Unlock()
	if terminalErr != nil {
		return proto.CloneOf(validated), terminalErr
	}
	return proto.CloneOf(validated), context.Cause(ctx)
}

// initializeReconnect serializes the SessionStore ownership CAS and registry
// commit for a stable client. Each retained registry entry owns one bounded
// context-aware gate that remains stable across epoch replacement.
func (registry *negotiatedSessionRegistry) initializeReconnect(
	ctx context.Context,
	clientID string,
	expectedEpoch uint64,
	response *enginev1.InitializeResponse,
	operation func() (daemon.Session, error),
) (*enginev1.InitializeResponse, error) {
	return registry.initializeReconnectAttempt(ctx, nil, clientID, expectedEpoch, response, operation)
}

func (registry *negotiatedSessionRegistry) initializeReconnectAttempt(
	ctx context.Context,
	attempt *initializationAttemptLease,
	clientID string,
	expectedEpoch uint64,
	response *enginev1.InitializeResponse,
	operation func() (daemon.Session, error),
) (*enginev1.InitializeResponse, error) {
	if registry == nil || operation == nil {
		return nil, errNegotiatedSessionClosed
	}
	validated, err := validateReconnectAttempt(ctx, attempt, clientID, expectedEpoch, response)
	if err != nil {
		return nil, err
	}
	gate, err := registry.acquireReconnectGate(ctx, clientID, expectedEpoch)
	if err != nil {
		return nil, err
	}
	defer registry.finishTransaction(false, gate, true)

	session, err := operation()
	if err != nil {
		return nil, err
	}
	if session.ClientID() != clientID || session.Epoch() != expectedEpoch+1 || session.Context() == nil {
		return nil, errNegotiatedSessionInvalid
	}
	committed, terminalErr := registry.commitReconnectSession(
		ctx, attempt, clientID, expectedEpoch, session, validated, gate,
	)
	if terminalErr != nil {
		if !committed {
			return nil, terminalErr
		}
		return proto.CloneOf(validated), terminalErr
	}
	return proto.CloneOf(validated), context.Cause(ctx)
}

func validateReconnectAttempt(
	ctx context.Context,
	attempt *initializationAttemptLease,
	clientID string,
	expectedEpoch uint64,
	response *enginev1.InitializeResponse,
) (*enginev1.InitializeResponse, error) {
	if ctx == nil || clientID == "" || expectedEpoch == 0 || expectedEpoch == math.MaxUint64 {
		return nil, errNegotiatedSessionInvalid
	}
	validated, err := validateReconnectResponse(clientID, expectedEpoch, response)
	if err != nil {
		return nil, err
	}
	if attempt != nil && string(validated.GetInitializationAttemptId()) != attempt.id {
		return nil, errNegotiatedSessionInvalid
	}
	return validated, nil
}

func (registry *negotiatedSessionRegistry) commitReconnectSession(
	ctx context.Context,
	attempt *initializationAttemptLease,
	clientID string,
	expectedEpoch uint64,
	session daemon.Session,
	response *enginev1.InitializeResponse,
	gate *initializationGate,
) (bool, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if err := registry.validateAttemptLeaseLocked(attempt); err != nil {
		return false, err
	}
	current, exists := registry.entries[clientID]
	if !exists || current.session.Epoch() != expectedEpoch || current.gate != gate {
		return false, errNegotiatedSessionUnavailable
	}
	registry.entries[clientID] = negotiatedSession{
		session: session, response: response, gate: gate,
		creationAttempt: current.creationAttempt, reconnectAttempt: current.reconnectAttempt,
	}
	if attempt != nil {
		if err := attempt.commitLocked(response, true); err != nil {
			// SessionStore already committed its ownership CAS. This path is
			// unreachable after the pre-CAS lease validation unless shutdown
			// fences the registry; fail closed rather than expose stale state.
			return true, err
		}
	}
	return true, registry.transactionTerminalError(ctx, session)
}

func (registry *negotiatedSessionRegistry) validateAttemptLeaseLocked(attempt *initializationAttemptLease) error {
	if attempt == nil {
		return nil
	}
	if attempt.registry != registry || attempt.record == nil || attempt.finished ||
		registry.attempts[attempt.id] != attempt.record || attempt.record.terminal {
		return errInitializationAttemptUnavailable
	}
	return nil
}

func validateReconnectResponse(
	clientID string,
	expectedEpoch uint64,
	response *enginev1.InitializeResponse,
) (*enginev1.InitializeResponse, error) {
	if response == nil {
		return nil, errNegotiatedSessionInvalid
	}
	validated := proto.CloneOf(response)
	if enginev1.ValidateInitializeResponse(validated) != nil || validated.GetClientId() != clientID ||
		validated.GetOwnershipEpoch() != expectedEpoch+1 {
		return nil, errNegotiatedSessionInvalid
	}
	return validated, nil
}

func (registry *negotiatedSessionRegistry) beginFreshTransaction(ctx context.Context) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closedLocked() {
		return errNegotiatedSessionClosed
	}
	if len(registry.entries)+registry.freshReservations >= registry.maximum {
		return &negotiatedCapacityError{
			target: errNegotiatedSessionCapacity, resource: "negotiated sessions",
			limit: uint64(registry.maximum), // #nosec G115 -- construction bounds maximum to 1..4096.
		}
	}
	registry.activeTransactions++
	registry.freshReservations++
	return nil
}

func (registry *negotiatedSessionRegistry) finishTransaction(
	fresh bool,
	gate *initializationGate,
	active bool,
) {
	registry.mu.Lock()
	if active && fresh && registry.freshReservations > 0 {
		registry.freshReservations--
	}
	if active && registry.activeTransactions > 0 {
		registry.activeTransactions--
	}
	if registry.closed && registry.activeTransactions == 0 {
		clear(registry.entries)
	}
	registry.mu.Unlock()
	if gate != nil {
		gate.token <- struct{}{}
	}
}

func newInitializationGate() *initializationGate {
	gate := &initializationGate{token: make(chan struct{}, 1)}
	gate.token <- struct{}{}
	return gate
}

func (registry *negotiatedSessionRegistry) acquireReconnectGate(
	ctx context.Context,
	clientID string,
	expectedEpoch uint64,
) (*initializationGate, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	gate, err := registry.reserveReconnectWaiter(clientID, expectedEpoch)
	if err != nil {
		return nil, err
	}

	var acquireErr error
	acquired := false
	select {
	case <-ctx.Done():
		acquireErr = context.Cause(ctx)
	case <-registry.root.Done():
		acquireErr = errNegotiatedSessionClosed
	case <-registry.closedCh:
		acquireErr = errNegotiatedSessionClosed
	case <-gate.token:
		acquired = true
	}
	if err = registry.finishReconnectAcquire(ctx, clientID, expectedEpoch, gate, acquireErr, acquired); err != nil {
		return nil, err
	}
	return gate, nil
}

func (registry *negotiatedSessionRegistry) finishReconnectAcquire(
	ctx context.Context,
	clientID string,
	expectedEpoch uint64,
	gate *initializationGate,
	acquireErr error,
	acquired bool,
) error {
	registry.mu.Lock()
	gate.waiters--
	current, exists := registry.entries[clientID]
	if acquireErr == nil {
		acquireErr = registry.reconnectAcquireStateError(ctx, current, exists, expectedEpoch, gate)
	}
	if acquireErr == nil {
		registry.activeTransactions++
	}
	registry.mu.Unlock()
	if acquireErr != nil && acquired {
		select {
		case gate.token <- struct{}{}:
		default:
		}
	}
	if acquireErr != nil {
		return acquireErr
	}
	return nil
}

func (registry *negotiatedSessionRegistry) reconnectAcquireStateError(
	ctx context.Context,
	current negotiatedSession,
	exists bool,
	expectedEpoch uint64,
	gate *initializationGate,
) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if registry.closedLocked() {
		return errNegotiatedSessionClosed
	}
	if !exists || current.session.Epoch() != expectedEpoch || current.gate != gate {
		return errNegotiatedSessionUnavailable
	}
	return nil
}

func (registry *negotiatedSessionRegistry) reserveReconnectWaiter(
	clientID string,
	expectedEpoch uint64,
) (*initializationGate, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	current, exists := registry.entries[clientID]
	if registry.closedLocked() {
		return nil, errNegotiatedSessionClosed
	}
	if !exists || current.session.Epoch() != expectedEpoch || current.gate == nil {
		return nil, errNegotiatedSessionUnavailable
	}
	gate := current.gate
	if gate.waiters >= maximumReconnectWaitersPerClient {
		return nil, &negotiatedCapacityError{
			target: errNegotiatedSessionWaiterLimit, resource: "reconnect initialization waiters",
			limit: maximumReconnectWaitersPerClient,
		}
	}
	gate.waiters++
	return gate, nil
}

func (registry *negotiatedSessionRegistry) transactionTerminalError(
	ctx context.Context,
	session daemon.Session,
) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if session.Context() == nil || session.Context().Err() != nil || registry.closedLocked() {
		return errNegotiatedSessionClosed
	}
	return nil
}

// replaceReconnect performs an exact registry compare-and-swap after
// SessionStore has advanced the same stable client to the next epoch.
func (registry *negotiatedSessionRegistry) replaceReconnect(
	clientID string,
	expectedEpoch uint64,
	next daemon.Session,
	response *enginev1.InitializeResponse,
) error {
	if registry == nil {
		return errNegotiatedSessionClosed
	}
	validated, err := validateNegotiatedSession(next, response)
	if err != nil || expectedEpoch == 0 || expectedEpoch == math.MaxUint64 ||
		next.ClientID() != clientID || next.Epoch() != expectedEpoch+1 {
		return errNegotiatedSessionInvalid
	}
	registry.mu.Lock()
	if registry.closedLocked() {
		registry.mu.Unlock()
		return errNegotiatedSessionClosed
	}
	current, exists := registry.entries[clientID]
	if !exists || current.session.Epoch() != expectedEpoch {
		registry.mu.Unlock()
		return errNegotiatedSessionUnavailable
	}
	replacement, err := registry.newEntryLocked(next, validated)
	if err != nil {
		registry.mu.Unlock()
		return err
	}
	replacement.gate = current.gate
	replacement.creationAttempt = current.creationAttempt
	replacement.reconnectAttempt = current.reconnectAttempt
	registry.entries[clientID] = replacement
	registry.mu.Unlock()
	return nil
}

// lookup requires an exact current epoch and never reveals whether a stable
// identity is unknown, stale, duplicated, or already fenced.
func (registry *negotiatedSessionRegistry) lookup(clientID string, epoch uint64) (negotiatedSession, error) {
	if registry == nil {
		return negotiatedSession{}, errNegotiatedSessionClosed
	}
	registry.mu.Lock()
	if registry.closedLocked() {
		registry.mu.Unlock()
		return negotiatedSession{}, errNegotiatedSessionClosed
	}
	entry, exists := registry.entries[clientID]
	if !exists || epoch == 0 || entry.session.Epoch() != epoch || entry.session.Context().Err() != nil {
		registry.mu.Unlock()
		return negotiatedSession{}, errNegotiatedSessionUnavailable
	}
	registry.mu.Unlock()
	return negotiatedSession{
		session:  entry.session,
		response: proto.CloneOf(entry.response),
	}, nil
}

func (registry *negotiatedSessionRegistry) newEntryLocked(
	session daemon.Session,
	response *enginev1.InitializeResponse,
) (negotiatedSession, error) {
	if registry.closedLocked() || session.Context().Err() != nil {
		return negotiatedSession{}, errNegotiatedSessionUnavailable
	}
	entry := negotiatedSession{session: session, response: response, gate: newInitializationGate()}
	if session.Context().Err() != nil {
		return negotiatedSession{}, errNegotiatedSessionUnavailable
	}
	return entry, nil
}

func validateNegotiatedSession(
	session daemon.Session,
	response *enginev1.InitializeResponse,
) (*enginev1.InitializeResponse, error) {
	if session.ClientID() == "" || session.Epoch() == 0 || session.Context() == nil || session.Context().Err() != nil ||
		response == nil {
		return nil, errNegotiatedSessionInvalid
	}
	cloned := proto.CloneOf(response)
	if enginev1.ValidateInitializeResponse(cloned) != nil || cloned.GetClientId() != session.ClientID() ||
		cloned.GetOwnershipEpoch() != session.Epoch() {
		return nil, errNegotiatedSessionInvalid
	}
	return cloned, nil
}

func (registry *negotiatedSessionRegistry) close() {
	if registry == nil {
		return
	}
	registry.closeOne.Do(func() {
		registry.mu.Lock()
		registry.closed = true
		close(registry.closedCh)
		registry.abortAttemptsLocked()
		if registry.activeTransactions == 0 {
			clear(registry.entries)
		}
		registry.mu.Unlock()

		if registry.stopRootWatch != nil {
			registry.stopRootWatch()
		}
	})
}

func (registry *negotiatedSessionRegistry) closedLocked() bool {
	return registry.closed || registry.root == nil || registry.root.Err() != nil
}
