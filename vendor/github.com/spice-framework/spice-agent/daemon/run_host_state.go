package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/client"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
	"github.com/spice-framework/spice-agent/interaction"
)

var (
	// ErrRunHostClosed rejects admission after lifecycle shutdown begins.
	ErrRunHostClosed = errors.New("run host is closed")
	// ErrRunHostCapacity reports that every configured active-run slot is reserved.
	ErrRunHostCapacity = errors.New("run host active capacity is exhausted")
	// ErrHostedRunUnavailable deliberately makes an unknown run and a run owned
	// by another stable client indistinguishable.
	ErrHostedRunUnavailable = errors.New("hosted run is unavailable")
	// ErrRunHostState rejects a known but illegal lifecycle transition.
	ErrRunHostState = errors.New("run host lifecycle transition is invalid")
	// ErrRunHostUncertain reports a durable transition whose outcome cannot be
	// proved. It is safe for clients but must never be retried under a new ID.
	ErrRunHostUncertain = errors.New("run host lifecycle outcome is uncertain")
	// ErrRunHostUnavailable reports a secret-safe dependency or persistence failure.
	ErrRunHostUnavailable = errors.New("run host dependency is unavailable")
)

// RunHostCapacityError reports one exact bounded host resource observation.
// It matches ErrRunHostCapacity while preserving facts needed by protocol
// recovery and overload diagnostics.
type RunHostCapacityError struct {
	resource string
	limit    uint64
	observed uint64
}

func (failure *RunHostCapacityError) Error() string {
	if failure == nil {
		return ErrRunHostCapacity.Error()
	}
	return fmt.Sprintf(
		"%s: %s limit %d, observed %d",
		ErrRunHostCapacity, failure.resource, failure.limit, failure.observed,
	)
}

// Is makes RunHostCapacityError match ErrRunHostCapacity.
func (failure *RunHostCapacityError) Is(target error) bool { return target == ErrRunHostCapacity }

// Resource returns the bounded host resource.
func (failure *RunHostCapacityError) Resource() string {
	if failure == nil {
		return ""
	}
	return failure.resource
}

// Limit returns the configured positive hard limit.
func (failure *RunHostCapacityError) Limit() uint64 {
	if failure == nil {
		return 0
	}
	return failure.limit
}

// Observed returns the rejected resource observation.
func (failure *RunHostCapacityError) Observed() uint64 {
	if failure == nil {
		return 0
	}
	return failure.observed
}

func newRunHostCapacity(resource string, limit, observed uint64) error {
	if boundedToken("capacity resource", resource) != nil || limit == 0 || observed <= limit {
		return ErrRunHostCapacity
	}
	return &RunHostCapacityError{resource: resource, limit: limit, observed: observed}
}

const (
	degradedAuthorityUncertain = "run authority outcome uncertain"
	degradedAuthorityMissing   = "run authority unavailable"
	degradedTerminalSnapshot   = "terminal snapshot unavailable"
	degradedLifecycleCleanup   = "run lifecycle cleanup unavailable"

	defaultTransitionTimeout = 5 * time.Second
	maximumTerminalRuns      = 1 << 16
	maximumTerminalBytes     = 1 << 30
)

// RunHostConfig owns the immutable limits and dependencies of a generated
// daemon application. Limits.ActiveRuns is the host's global active capacity,
// so the same value is reported by Health without caller-controlled drift.
type RunHostConfig struct {
	Root              context.Context //nolint:containedctx // transferred as the daemon lifetime root, never as request state.
	Engine            *agent.Engine
	Authority         *RunAuthority
	Sessions          *SessionStore
	Ledger            *Ledger
	Pending           *PendingHub
	Definitions       DefinitionSet
	HealthSources     []HealthSource
	Limits            client.Limits
	TerminalRuns      int
	TerminalBytes     int
	TransitionTimeout time.Duration
}

type hostAuthority interface {
	Start(context.Context, string) (hostActiveAuthority, error)
	PrepareImport(context.Context, *enginev1.SnapshotEnvelope) (hostImportAuthority, error)
	Close() error
}

type hostActiveAuthority interface {
	Resume(context.Context) error
	IssueSnapshotEnvelope(context.Context, agent.Snapshot) (*enginev1.SnapshotEnvelope, error)
	Terminal(context.Context, TerminalPhase) error
	Close() error
}

type hostImportAuthority interface {
	enginev1.SnapshotAuthorityVerifier
	Consume(context.Context) error
	Activate(context.Context) (hostActiveAuthority, error)
	Abort() error
}

type concreteHostImport struct{ value *RunImport }

func (transaction concreteHostImport) VerifySnapshot(
	ctx context.Context,
	input enginev1.SnapshotAuthorityInput,
	claim *enginev1.SnapshotAuthority,
) error {
	return transaction.value.VerifySnapshot(ctx, input, claim)
}

func (transaction concreteHostImport) Consume(ctx context.Context) error {
	return transaction.value.Consume(ctx)
}

func (transaction concreteHostImport) Activate(ctx context.Context) (hostActiveAuthority, error) {
	return transaction.value.Activate(ctx)
}

func (transaction concreteHostImport) Abort() error { return transaction.value.Abort() }

// Go does not permit covariant interface returns, so adapt the concrete import
// explicitly instead of exporting an abstraction solely for tests.
type concreteAuthorityAdapter struct{ value *RunAuthority }

func (authority concreteAuthorityAdapter) Start(ctx context.Context, runID string) (hostActiveAuthority, error) {
	return authority.value.Start(ctx, runID)
}

func (authority concreteAuthorityAdapter) PrepareImport(
	ctx context.Context,
	envelope *enginev1.SnapshotEnvelope,
) (hostImportAuthority, error) {
	value, err := authority.value.PrepareImport(ctx, envelope)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, ErrRunAuthorityUnavailable
	}
	return concreteHostImport{value: value}, nil
}

func (authority concreteAuthorityAdapter) Close() error { return authority.value.Close() }

type hostedRun struct {
	clientID  string
	run       *agent.Run
	authority hostActiveAuthority
	binding   *RunBinding

	transition *runTransition
	snapshot   *enginev1.SnapshotEnvelope
}

type terminalRun struct {
	clientID string
	run      *agent.Run
	envelope *enginev1.SnapshotEnvelope
	sequence uint64
	bytes    int
	ordinal  uint64
}

type hostPausedRun struct {
	mu       sync.Mutex
	prepared *agent.PreparedRun
	decided  bool
}

type hostReservation struct {
	host     *RunHost
	clientID string
	runID    string
	released bool
	bound    bool
	mu       sync.Mutex
}

// RunHost is the transport-independent lifecycle owner. It contains no RPC,
// socket, or client-stream machinery; future transports can expose epoch-bound
// sessions around these methods without duplicating lifecycle transitions.
type RunHost struct {
	root       context.Context //nolint:containedctx // daemon lifetime is owned here.
	cancelRoot context.CancelCauseFunc

	engine        *agent.Engine
	authority     hostAuthority
	sessions      *SessionStore
	ledger        *Ledger
	pending       *PendingHub
	definitions   DefinitionSet
	healthSources []HealthSource
	limits        client.Limits

	transitionTimeout time.Duration
	terminalMaxRuns   int
	terminalMaxBytes  int

	mu             sync.Mutex
	closing        bool
	activeReserved uint64
	active         map[string]*hostedRun
	terminal       map[string]*terminalRun
	terminalOrder  []string
	terminalBytes  int
	nextOrdinal    uint64
	reservations   map[string]string
	owners         map[string]string
	paused         map[*hostPausedRun]struct{}
	degraded       map[string]struct{}

	operations   sync.WaitGroup
	monitors     sync.WaitGroup
	shutdown     sync.Once
	shutdownDone chan struct{}
	shutdownMu   sync.Mutex
	shutdownErr  error
}

// NewRunHost validates and takes ownership of all configured lifecycle
// dependencies. Shutdown closes/drains them in dependency order.
func NewRunHost(config RunHostConfig) (*RunHost, error) {
	if config.Authority == nil {
		return nil, errors.New("run host authority is nil")
	}
	return newRunHost(config, concreteAuthorityAdapter{value: config.Authority})
}

func newRunHost(config RunHostConfig, authority hostAuthority) (*RunHost, error) {
	if config.Root == nil || config.Engine == nil || authority == nil || config.Sessions == nil ||
		config.Ledger == nil || config.Pending == nil {
		return nil, errors.New("run host requires root, engine, authority, sessions, ledger, and pending hub")
	}
	healthSources, sourceErr := cloneHealthSources(config.HealthSources)
	if sourceErr != nil {
		return nil, sourceErr
	}
	if err := config.Root.Err(); err != nil {
		return nil, errors.New("run host root is already canceled")
	}
	if err := validateRunHostLimits(config.Pending, config.Limits); err != nil {
		return nil, err
	}
	definitions, err := NewDefinitionSet(config.Definitions.Definitions())
	if err != nil || definitions.Revision() != config.Definitions.Revision() {
		return nil, errors.New("run host definition set is invalid")
	}
	if config.TerminalRuns < 1 || config.TerminalRuns > maximumTerminalRuns {
		return nil, fmt.Errorf("run host terminal count must be between 1 and %d", maximumTerminalRuns)
	}
	if config.TerminalBytes < 1 || config.TerminalBytes > maximumTerminalBytes {
		return nil, fmt.Errorf("run host terminal bytes must be between 1 and %d", maximumTerminalBytes)
	}
	timeout := config.TransitionTimeout
	if timeout == 0 {
		timeout = defaultTransitionTimeout
	}
	if timeout < time.Millisecond || timeout > 30*time.Second {
		return nil, errors.New("run host transition timeout must be between 1ms and 30s")
	}
	root, cancel := context.WithCancelCause(config.Root)
	return &RunHost{
		root: root, cancelRoot: cancel,
		engine: config.Engine, authority: authority, sessions: config.Sessions,
		ledger: config.Ledger, pending: config.Pending, definitions: definitions,
		healthSources: healthSources,
		limits:        config.Limits, transitionTimeout: timeout,
		terminalMaxRuns: config.TerminalRuns, terminalMaxBytes: config.TerminalBytes,
		active: make(map[string]*hostedRun), terminal: make(map[string]*terminalRun),
		reservations: make(map[string]string), owners: make(map[string]string),
		paused:       make(map[*hostPausedRun]struct{}),
		degraded:     make(map[string]struct{}),
		shutdownDone: make(chan struct{}),
	}, nil
}

func validateRunHostLimits(pending *PendingHub, limits client.Limits) error {
	if err := limits.Validate(); err != nil {
		return fmt.Errorf("run host limits: %w", err)
	}
	if limits.ConcurrentStreams() > maximumSessionStreamsPerClient {
		return fmt.Errorf("run host concurrent streams exceed %d", maximumSessionStreamsPerClient)
	}
	pendingItems, pendingBytes := pending.snapshotCapacityUpperBound()
	if pendingItems < 1 || uint64(pendingItems) > uint64(limits.CollectionItems()) ||
		pendingBytes < 1 || uint64(pendingBytes) > limits.MessageBytes() {
		return errors.New("run host pending snapshot capacity exceeds protocol limits")
	}
	return nil
}

func (host *RunHost) beginOperation() error {
	if host == nil {
		return ErrRunHostClosed
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closing || host.root.Err() != nil {
		return ErrRunHostClosed
	}
	host.operations.Add(1)
	return nil
}

func (host *RunHost) endOperation() { host.operations.Done() }

func (host *RunHost) transitionContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(host.root), host.transitionTimeout)
}

func (host *RunHost) reserveSlot(clientID string) (*hostReservation, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.closing || host.root.Err() != nil {
		return nil, ErrRunHostClosed
	}
	if host.activeReserved >= uint64(host.limits.ActiveRuns()) {
		limit := uint64(host.limits.ActiveRuns())
		return nil, newRunHostCapacity("active runs", limit, host.activeReserved+1)
	}
	host.activeReserved++
	return &hostReservation{host: host, clientID: clientID}, nil
}

func (reservation *hostReservation) bind(runID string) (*RunBinding, error) {
	if reservation == nil || reservation.host == nil {
		return nil, ErrRunHostState
	}
	scope, err := interaction.NewScope(runID)
	if err != nil {
		return nil, ErrRunHostState
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.released || reservation.bound {
		return nil, ErrRunHostState
	}
	host := reservation.host
	host.mu.Lock()
	if host.closing || host.root.Err() != nil {
		host.mu.Unlock()
		return nil, ErrRunHostClosed
	}
	if _, exists := host.owners[runID]; exists {
		host.mu.Unlock()
		return nil, ErrRunHostState
	}
	if _, exists := host.reservations[runID]; exists {
		host.mu.Unlock()
		return nil, ErrRunHostState
	}
	host.reservations[runID] = reservation.clientID
	host.mu.Unlock()

	binding, err := host.pending.BindRun(reservation.clientID, scope)
	if err != nil {
		host.mu.Lock()
		delete(host.reservations, runID)
		host.mu.Unlock()
		return nil, publicRunHostError(err)
	}
	reservation.runID = runID
	reservation.bound = true
	return binding, nil
}

func (reservation *hostReservation) release() {
	if reservation == nil || reservation.host == nil {
		return
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.released {
		return
	}
	reservation.released = true
	host := reservation.host
	host.mu.Lock()
	if reservation.bound {
		delete(host.reservations, reservation.runID)
	}
	if host.activeReserved > 0 {
		host.activeReserved--
	}
	host.mu.Unlock()
}

func (host *RunHost) trackPaused(prepared *agent.PreparedRun) *hostPausedRun {
	candidate := &hostPausedRun{prepared: prepared}
	host.mu.Lock()
	host.paused[candidate] = struct{}{}
	host.mu.Unlock()
	return candidate
}

func (host *RunHost) untrackPaused(candidate *hostPausedRun) {
	host.mu.Lock()
	delete(host.paused, candidate)
	host.mu.Unlock()
}

func (host *RunHost) publish(
	reservation *hostReservation,
	run *agent.Run,
	authority hostActiveAuthority,
	binding *RunBinding,
) {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	value := &hostedRun{
		clientID: reservation.clientID, run: run, authority: authority,
		binding: binding, transition: newRunTransition(),
	}
	host.mu.Lock()
	delete(host.reservations, run.ID())
	host.active[run.ID()] = value
	host.owners[run.ID()] = reservation.clientID
	host.monitors.Add(1)
	host.mu.Unlock()
	reservation.released = true
	go host.monitor(value)
}

func (host *RunHost) ownedActive(clientID, runID string) (*hostedRun, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	value := host.active[runID]
	if value == nil || value.clientID != clientID {
		return nil, ErrHostedRunUnavailable
	}
	return value, nil
}

func (host *RunHost) ownedRun(clientID, runID string) (*hostedRun, *terminalRun, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if value := host.active[runID]; value != nil && value.clientID == clientID {
		return value, nil, nil
	}
	if value := host.terminal[runID]; value != nil && value.clientID == clientID {
		return nil, value, nil
	}
	return nil, nil, ErrHostedRunUnavailable
}

func (host *RunHost) owns(clientID, runID string) bool {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.owners[runID] == clientID
}

func (host *RunHost) degrade(reason string) {
	switch reason {
	case degradedAuthorityUncertain, degradedAuthorityMissing,
		degradedTerminalSnapshot, degradedLifecycleCleanup:
	default:
		// This private path is deliberately closed over fixed public-safe
		// reasons. Never let arbitrary dependency text enter client health.
		reason = degradedLifecycleCleanup
	}
	host.mu.Lock()
	host.degraded[reason] = struct{}{}
	host.mu.Unlock()
}

func publicRunHostError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	if hostCapacity, ok := errors.AsType[*RunHostCapacityError](err); ok {
		return hostCapacity
	}
	if identityCapacity, ok := errors.AsType[*agent.RunIdentityCapacityError](err); ok {
		return newRunHostCapacity("run identities", identityCapacity.Limit(), identityCapacity.Observed())
	}
	return publicRunHostOwnerError(err)
}

func publicRunHostOwnerError(err error) error {
	switch {
	case errors.Is(err, ErrRunHostCapacity):
		return ErrRunHostCapacity
	case errors.Is(err, ErrHostedRunUnavailable):
		return ErrHostedRunUnavailable
	case errors.Is(err, ErrRunHostUncertain):
		return ErrRunHostUncertain
	case errors.Is(err, ErrRunHostUnavailable):
		return ErrRunHostUnavailable
	case errors.Is(err, ErrRunHostState):
		return ErrRunHostState
	case errors.Is(err, ErrRunHostClosed), errors.Is(err, ErrSessionStoreClosed), errors.Is(err, ErrPendingHubClosed):
		return ErrRunHostClosed
	}
	return publicRunHostDependencyError(err)
}

func publicRunHostDependencyError(err error) error {
	if stale, ok := errors.AsType[*StaleSessionError](err); ok {
		return stale
	}
	if errors.Is(err, ErrStaleSession) {
		return ErrStaleSession
	}
	if sessionCapacity, ok := errors.AsType[*SessionGateCapacityError](err); ok {
		return sessionCapacity
	}
	switch {
	case errors.Is(err, ErrSessionGateCapacity):
		return ErrRunHostCapacity
	case errors.Is(err, ErrRunAuthorityUncertain):
		return ErrRunHostUncertain
	case errors.Is(err, ErrRunAuthorityUnavailable):
		return ErrRunHostUnavailable
	case errors.Is(err, ErrRunAuthorityBusy), errors.Is(err, ErrRunAuthorityState),
		errors.Is(err, ErrRunAuthorityVerification):
		return ErrRunHostState
	case errors.Is(err, ErrRunBindingCapacity), errors.Is(err, ErrPendingCapacity),
		errors.Is(err, ErrObserverCapacity):
		return ErrRunHostCapacity
	case errors.Is(err, ErrRunNotBound), errors.Is(err, ErrInteractionNotPending):
		return ErrHostedRunUnavailable
	default:
		return ErrRunHostState
	}
}

// isIsolatedContextTermination accepts a cancellation sentinel through a
// single-cause context chain. A multi-cause join can carry cleanup or commit
// uncertainty alongside cancellation and must remain fail-closed.
func isIsolatedContextTermination(err error) bool {
	if err == nil {
		return false
	}
	for {
		if _, wrapsMany := err.(interface{ Unwrap() []error }); wrapsMany {
			return false
		}
		wrapped, wraps := err.(interface{ Unwrap() error })
		if !wraps {
			return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		}
		err = wrapped.Unwrap()
		if err == nil {
			return false
		}
	}
}
