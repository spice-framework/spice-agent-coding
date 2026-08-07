package daemon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"

	"github.com/spice-framework/spice-agent/interaction"
)

var (
	// ErrPendingHubClosed rejects work after daemon-root shutdown.
	ErrPendingHubClosed = errors.New("pending interaction hub is closed")
	// ErrRunNotBound rejects interaction work for a run without an active or
	// draining client binding.
	ErrRunNotBound = errors.New("interaction run is not bound to a client")
	// ErrObserverFenced terminates discovery streams owned by a superseded
	// client connection. Pending interactions remain available to reconnect.
	ErrObserverFenced = errors.New("pending interaction observers were fenced")
	// ErrRunAlreadyBound rejects duplicate stable ownership of one run.
	ErrRunAlreadyBound = errors.New("interaction run is already bound")
	// ErrRunBindingCapacity rejects a run lease beyond configured bounds.
	ErrRunBindingCapacity = errors.New("pending run binding capacity exhausted")
	// ErrPendingCapacity rejects a pending interaction beyond configured count
	// or byte bounds.
	ErrPendingCapacity = errors.New("pending interaction capacity exhausted")
	// ErrObserverCapacity rejects a discovery stream beyond configured observer
	// or queue-reservation bounds.
	ErrObserverCapacity = errors.New("pending observer capacity exhausted")
	// ErrInteractionNotPending rejects a response without a matching open call.
	ErrInteractionNotPending = errors.New("interaction is not pending")
)

var errPendingClientCapacity = errors.New("pending client capacity exhausted")

var closedDeltaStream = func() <-chan Delta {
	stream := make(chan Delta)
	close(stream)
	return stream
}()

const (
	maximumPendingClients        = 4096
	maximumPendingRuns           = 16384
	maximumPendingInteractions   = 4096
	maximumPendingObservers      = 1024
	maximumObserverQueue         = 1024
	maximumObserverEntries       = maximumPendingObservers * maximumObserverQueue
	maximumObserverQueuedBytes   = 4 << 20
	maximumPendingBytes          = 16 << 20
	maximumObserverBytes         = 64 << 20
	maximumPendingDeltaBytes     = 2*interaction.MaximumPayloadBytes + 512
	pendingSnapshotItemOverhead  = 32
	pendingSnapshotFrameOverhead = 32
)

// PendingLimits bounds both the whole hub and every stable client partition.
// A subscription reserves its entire queue budget until it terminates.
type PendingLimits struct {
	Clients                       int
	Runs                          int
	RunsPerClient                 int
	Pending                       int
	PendingPerClient              int
	PendingBytes                  int
	PendingBytesPerClient         int
	Observers                     int
	ObserversPerClient            int
	ObserverQueueEntries          int
	ObserverQueueBytes            int
	ReservedQueueEntries          int
	ReservedQueueEntriesPerClient int
	ReservedQueueBytes            int
	ReservedQueueBytesPerClient   int
	QueuedEntries                 int
	QueuedEntriesPerClient        int
	QueuedBytes                   int
	QueuedBytesPerClient          int
}

// DefaultPendingLimits returns conservative production defaults. Every
// observer can retain the largest valid delta, and aggregate actual queue caps
// cover all capacity reserved when subscriptions are admitted.
func DefaultPendingLimits() PendingLimits {
	const (
		observers          = 32
		observersPerClient = 4
		queueEntries       = 64
	)
	return PendingLimits{
		Clients: 1024, Runs: 4096, RunsPerClient: 256,
		Pending: 1024, PendingPerClient: 128,
		PendingBytes: maximumPendingBytes, PendingBytesPerClient: 768 << 10,
		Observers: observers, ObserversPerClient: observersPerClient,
		ObserverQueueEntries: queueEntries, ObserverQueueBytes: maximumPendingDeltaBytes,
		ReservedQueueEntries:          observers * queueEntries,
		ReservedQueueEntriesPerClient: observersPerClient * queueEntries,
		ReservedQueueBytes:            observers * maximumPendingDeltaBytes,
		ReservedQueueBytesPerClient:   observersPerClient * maximumPendingDeltaBytes,
		QueuedEntries:                 observers * queueEntries,
		QueuedEntriesPerClient:        observersPerClient * queueEntries,
		QueuedBytes:                   observers * maximumPendingDeltaBytes,
		QueuedBytesPerClient:          observersPerClient * maximumPendingDeltaBytes,
	}
}

// snapshotCapacityUpperBound returns the maximum complete transport-neutral
// pending-set shape admitted for one client. The fixed overhead safely covers
// every current Protobuf field tag, length prefix, revision, repeated-item
// wrapper, and stream-response oneof wrapper without importing wire packages.
func (hub *PendingHub) snapshotCapacityUpperBound() (int, int) {
	if hub == nil {
		return 0, 0
	}
	items := hub.limits.PendingPerClient
	bytes := hub.limits.PendingBytesPerClient + items*pendingSnapshotItemOverhead + pendingSnapshotFrameOverhead
	return items, bytes
}

// ObserverExhaustedError reports the exact last revision and queue bound seen
// by a slow subscriber. Recovery creates a new client-scoped subscription and
// consumes its mandatory complete snapshot.
type ObserverExhaustedError struct {
	LastDelivered uint64
	resource      string
	limit         int
	observed      int
}

func (failure *ObserverExhaustedError) Error() string {
	return fmt.Sprintf(
		"pending interaction observer exceeded %s limit %d with %d after revision %d",
		failure.resource, failure.limit, failure.observed, failure.LastDelivered,
	)
}

// Resource identifies the exact safe protocol resource that was exhausted.
func (failure *ObserverExhaustedError) Resource() string {
	if failure == nil {
		return ""
	}
	return failure.resource
}

// Limit returns the exact configured bound that was exceeded.
func (failure *ObserverExhaustedError) Limit() uint64 {
	if failure == nil || failure.limit < 0 {
		return 0
	}
	return uint64(failure.limit)
}

// Observed returns the exact refused count or byte size.
func (failure *ObserverExhaustedError) Observed() uint64 {
	if failure == nil || failure.observed < 0 {
		return 0
	}
	return uint64(failure.observed)
}

func newObserverExhausted(resource string, limit, observed int) *ObserverExhaustedError {
	return &ObserverExhaustedError{resource: resource, limit: limit, observed: observed}
}

// Pending is one immutable pending interaction discovery value.
type Pending struct {
	Scope   interaction.Scope
	Request interaction.Request
}

// DeltaKind identifies a pending-interaction lifecycle mutation.
type DeltaKind string

const (
	// DeltaOpened adds a newly pending request after the complete snapshot.
	DeltaOpened DeltaKind = "opened"
	// DeltaClosed removes a completed or canceled request.
	DeltaClosed DeltaKind = "closed"
)

// Delta is one revision-contiguous mutation within one stable client.
type Delta struct {
	Revision uint64
	Kind     DeltaKind
	Pending  Pending
}

// PendingSnapshot is the mandatory complete first client-scoped view.
type PendingSnapshot struct {
	Revision uint64
	Pending  []Pending
}

type pendingKey struct {
	runID         string
	interactionID interaction.ID
}

type pendingResult struct {
	response interaction.Response
	err      error
}

type pendingCall struct {
	value   Pending
	binding *runBindingState
	done    chan struct{}
	result  pendingResult
}

type runBindingState struct {
	clientID  string
	runID     string
	accepting bool
	active    int
	released  chan struct{}
}

type pendingPartition struct {
	revision        uint64
	nextWatcher     uint64
	pendingBytes    int
	queuedBytes     int
	reservedBytes   int
	boundRuns       int
	queuedEntries   int
	reservedEntries int
	pending         map[pendingKey]*pendingCall
	watchers        map[uint64]*pendingWatcher
	stopping        map[*pendingWatcher]struct{}
}

type queuedDelta struct {
	delta Delta
	bytes int
}

type pendingWatcher struct {
	hub           *PendingHub
	partition     *pendingPartition
	id            uint64
	queue         chan queuedDelta
	out           chan Delta
	stop          chan struct{}
	done          chan struct{}
	active        bool
	err           error
	exhausted     bool
	lastDelivered uint64
	queuedCount   int
	queuedBytes   int
	stopContext   func() bool
}

// PendingSubscription atomically couples a complete client snapshot with a
// live tail registered at exactly that partition revision.
type PendingSubscription struct {
	hub      *PendingHub
	snapshot PendingSnapshot
	watcher  *pendingWatcher
}

// Snapshot returns the complete immutable first frame.
func (subscription *PendingSubscription) Snapshot() PendingSnapshot {
	if subscription == nil {
		return PendingSnapshot{}
	}
	return clonePendingSnapshot(subscription.snapshot)
}

// Deltas returns revision-contiguous changes following Snapshot.
func (subscription *PendingSubscription) Deltas() <-chan Delta {
	if subscription == nil || subscription.watcher == nil {
		return closedDeltaStream
	}
	return subscription.watcher.out
}

// LastDelivered returns the exact revision most recently sent to the consumer,
// or the snapshot revision before the first delta.
func (subscription *PendingSubscription) LastDelivered() uint64 {
	if subscription == nil || subscription.hub == nil || subscription.watcher == nil {
		return 0
	}
	subscription.hub.mu.Lock()
	defer subscription.hub.mu.Unlock()
	return subscription.watcher.lastDelivered
}

// Wait reports hub shutdown, subscriber cancellation, reconnect fencing, or
// typed queue exhaustion.
func (subscription *PendingSubscription) Wait(ctx context.Context) error {
	if subscription == nil || subscription.watcher == nil {
		return errors.New("pending subscription is nil")
	}
	if ctx == nil {
		return errors.New("pending subscription wait context is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-subscription.watcher.done:
		return subscription.terminalError()
	}
}

func (subscription *PendingSubscription) terminalError() error {
	subscription.hub.mu.Lock()
	defer subscription.hub.mu.Unlock()
	if subscription.watcher.exhausted {
		failure, ok := errors.AsType[*ObserverExhaustedError](subscription.watcher.err)
		if !ok {
			return errors.New("pending interaction observer exhausted without recovery facts")
		}
		result := *failure
		result.LastDelivered = subscription.watcher.lastDelivered
		return &result
	}
	return subscription.watcher.err
}

// RunBinding is the exclusive stable-client ownership lease for one run. Its
// release stops new requests but deliberately leaves accepted requests
// respondable until they finish, preventing a run-terminal race.
type RunBinding struct {
	hub   *PendingHub
	state *runBindingState
	once  sync.Once
}

// Release prevents new requests. Capacity is reclaimed after every already
// accepted interaction reaches its own terminal result.
func (binding *RunBinding) Release() {
	if binding == nil || binding.hub == nil || binding.state == nil {
		return
	}
	binding.once.Do(func() {
		binding.hub.mu.Lock()
		binding.state.accepting = false
		binding.hub.removeReleasedBindingLocked(binding.state)
		binding.hub.mu.Unlock()
	})
}

// ClientID returns the stable client identity owning the lease.
func (binding *RunBinding) ClientID() string {
	if binding == nil || binding.state == nil {
		return ""
	}
	return binding.state.clientID
}

// RunID returns the bound run identity.
func (binding *RunBinding) RunID() string {
	if binding == nil || binding.state == nil {
		return ""
	}
	return binding.state.runID
}

// WaitReleased waits until Release has stopped admission and all interactions
// accepted before that point have reached a terminal result.
func (binding *RunBinding) WaitReleased(ctx context.Context) error {
	if binding == nil || binding.state == nil {
		return errors.New("pending run binding is nil")
	}
	if ctx == nil {
		return errors.New("pending run binding wait context is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-binding.state.released:
		return nil
	}
}

// PendingHub implements interaction.Broker while partitioning discovery by
// stable client identity. One lock owns routing, revisions, caps, queue
// reservations, and accounting.
type PendingHub struct {
	mu              sync.Mutex
	limits          PendingLimits
	pendingCount    int
	pendingBytes    int
	observerCount   int
	queuedEntries   int
	queuedBytes     int
	reservedEntries int
	reservedBytes   int
	partitions      map[string]*pendingPartition
	runs            map[string]*runBindingState
	watcherWG       sync.WaitGroup
	closeDone       chan struct{}
	closed          bool
}

var _ interaction.Broker = (*PendingHub)(nil)

// NewPendingHub constructs a bounded, client-partitioned interaction hub.
func NewPendingHub(limits PendingLimits) (*PendingHub, error) {
	if err := validatePendingLimits(limits); err != nil {
		return nil, err
	}
	return &PendingHub{
		limits:     limits,
		partitions: make(map[string]*pendingPartition), runs: make(map[string]*runBindingState),
		closeDone: make(chan struct{}),
	}, nil
}

func validatePendingLimits(limits PendingLimits) error {
	if err := validatePendingCountLimits(limits); err != nil {
		return err
	}
	if err := validatePendingByteLimits(limits); err != nil {
		return err
	}
	return validatePendingReservationFunding(limits)
}

func validatePendingCountLimits(limits PendingLimits) error {
	if limits.Clients < 1 || limits.Clients > maximumPendingClients {
		return fmt.Errorf("pending clients must be within [1,%d]", maximumPendingClients)
	}
	for _, bound := range []struct {
		label     string
		global    int
		perClient int
		maximum   int
	}{
		{"runs", limits.Runs, limits.RunsPerClient, maximumPendingRuns},
		{"interactions", limits.Pending, limits.PendingPerClient, maximumPendingInteractions},
		{"observers", limits.Observers, limits.ObserversPerClient, maximumPendingObservers},
	} {
		if err := validateGlobalClientBound(bound.label, bound.global, bound.perClient, bound.maximum); err != nil {
			return err
		}
	}
	if limits.ObserverQueueEntries < 1 || limits.ObserverQueueEntries > maximumObserverQueue {
		return fmt.Errorf("pending observer queue entries must be within [1,%d]", maximumObserverQueue)
	}
	if err := validateGlobalClientBound(
		"reserved queue entries", limits.ReservedQueueEntries,
		limits.ReservedQueueEntriesPerClient, maximumObserverEntries,
	); err != nil {
		return err
	}
	if err := validateGlobalClientBound(
		"actual queue entries", limits.QueuedEntries,
		limits.QueuedEntriesPerClient, maximumObserverEntries,
	); err != nil {
		return err
	}
	if limits.ReservedQueueEntries < limits.ObserverQueueEntries ||
		limits.ReservedQueueEntriesPerClient < limits.ObserverQueueEntries {
		return errors.New("pending reserved queue entries must cover one observer queue")
	}
	if limits.QueuedEntries < limits.ReservedQueueEntries ||
		limits.QueuedEntriesPerClient < limits.ReservedQueueEntriesPerClient {
		return errors.New("pending actual queue entries must cover reserved queue entries")
	}
	return nil
}

func validatePendingByteLimits(limits PendingLimits) error {
	if err := validateGlobalClientBound(
		"pending bytes", limits.PendingBytes, limits.PendingBytesPerClient, maximumPendingBytes,
	); err != nil {
		return err
	}
	if limits.ObserverQueueBytes < 1 || limits.ObserverQueueBytes > maximumObserverQueuedBytes {
		return fmt.Errorf("pending observer queue bytes must be within [1,%d]", maximumObserverQueuedBytes)
	}
	if err := validateGlobalClientBound(
		"reserved queue bytes", limits.ReservedQueueBytes,
		limits.ReservedQueueBytesPerClient, maximumObserverBytes,
	); err != nil {
		return err
	}
	if err := validateGlobalClientBound(
		"actual queue bytes", limits.QueuedBytes, limits.QueuedBytesPerClient, maximumObserverBytes,
	); err != nil {
		return err
	}
	if limits.ReservedQueueBytes < limits.ObserverQueueBytes ||
		limits.ReservedQueueBytesPerClient < limits.ObserverQueueBytes {
		return errors.New("pending reserved queue bytes must cover one observer queue")
	}
	if limits.QueuedBytes < limits.ReservedQueueBytes ||
		limits.QueuedBytesPerClient < limits.ReservedQueueBytesPerClient {
		return errors.New("pending actual queue bytes must cover reserved queue bytes")
	}
	return nil
}

func validateGlobalClientBound(label string, global, perClient, maximum int) error {
	if global < 1 || global > maximum || perClient < 1 || perClient > global {
		return fmt.Errorf("pending %s must be within [1,%d] with per-client no greater than global", label, maximum)
	}
	return nil
}

func validatePendingReservationFunding(limits PendingLimits) error {
	if limits.ReservedQueueEntries/limits.ObserverQueueEntries < limits.Observers ||
		limits.ReservedQueueEntriesPerClient/limits.ObserverQueueEntries < limits.ObserversPerClient ||
		limits.ReservedQueueBytes/limits.ObserverQueueBytes < limits.Observers ||
		limits.ReservedQueueBytesPerClient/limits.ObserverQueueBytes < limits.ObserversPerClient {
		return errors.New("pending global queue reservations cannot cover the configured observer bound")
	}
	return nil
}

// BindRun exclusively assigns a run to one stable client. The returned lease
// must be released after the run terminates.
func (hub *PendingHub) BindRun(clientID string, scope interaction.Scope) (*RunBinding, error) {
	if hub == nil {
		return nil, ErrPendingHubClosed
	}
	if err := boundedToken("client ID", clientID); err != nil {
		return nil, err
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return nil, ErrPendingHubClosed
	}
	if _, exists := hub.runs[scope.RunID()]; exists {
		return nil, fmt.Errorf("%w: %s", ErrRunAlreadyBound, scope.RunID())
	}
	if len(hub.runs) >= hub.limits.Runs {
		return nil, ErrRunBindingCapacity
	}
	partition, err := hub.partitionLocked(clientID, true)
	if err != nil {
		if errors.Is(err, errPendingClientCapacity) {
			return nil, fmt.Errorf("%w: client partitions", ErrRunBindingCapacity)
		}
		return nil, err
	}
	if partition == nil {
		return nil, fmt.Errorf("%w: client partition unavailable", ErrRunBindingCapacity)
	}
	if partition.boundRuns >= hub.limits.RunsPerClient {
		return nil, ErrRunBindingCapacity
	}
	state := &runBindingState{
		clientID: clientID, runID: scope.RunID(), accepting: true, released: make(chan struct{}),
	}
	hub.runs[state.runID] = state
	partition.boundRuns++
	return &RunBinding{hub: hub, state: state}, nil
}

func (hub *PendingHub) partitionLocked(clientID string, create bool) (*pendingPartition, error) {
	partition := hub.partitions[clientID]
	if partition != nil || !create {
		return partition, nil
	}
	if len(hub.partitions) >= hub.limits.Clients {
		return nil, errPendingClientCapacity
	}
	partition = &pendingPartition{
		pending:  make(map[pendingKey]*pendingCall),
		watchers: make(map[uint64]*pendingWatcher), stopping: make(map[*pendingWatcher]struct{}),
	}
	hub.partitions[clientID] = partition
	return partition, nil
}

func makePendingKey(scope interaction.Scope, id interaction.ID) pendingKey {
	return pendingKey{runID: scope.RunID(), interactionID: id}
}

// Request publishes and awaits one interaction in its bound client partition.
func (hub *PendingHub) Request(ctx context.Context, scope interaction.Scope, request interaction.Request) (interaction.Response, error) {
	if hub == nil {
		return interaction.Response{}, ErrPendingHubClosed
	}
	if ctx == nil {
		return interaction.Response{}, errors.New("pending interaction context is nil")
	}
	if err := ctx.Err(); err != nil {
		return interaction.Response{}, err
	}
	if err := scope.Validate(); err != nil {
		return interaction.Response{}, err
	}
	if err := request.Validate(); err != nil {
		return interaction.Response{}, err
	}
	key := makePendingKey(scope, request.ID())
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return interaction.Response{}, ErrPendingHubClosed
	}
	binding := hub.runs[scope.RunID()]
	if binding == nil || !binding.accepting {
		hub.mu.Unlock()
		return interaction.Response{}, ErrRunNotBound
	}
	partition := hub.partitions[binding.clientID]
	if partition == nil {
		hub.mu.Unlock()
		return interaction.Response{}, ErrRunNotBound
	}
	if _, exists := partition.pending[key]; exists {
		hub.mu.Unlock()
		return interaction.Response{}, errors.New("interaction is already pending")
	}
	size := pendingValueBytes(Pending{Scope: scope, Request: request})
	if !hub.hasPendingCapacity(partition, size) {
		hub.mu.Unlock()
		return interaction.Response{}, ErrPendingCapacity
	}
	required := uint64(len(partition.pending)) + 2
	if partition.revision > math.MaxUint64-required {
		hub.mu.Unlock()
		return interaction.Response{}, fmt.Errorf("%w: revision exhausted", ErrPendingCapacity)
	}
	call := &pendingCall{
		value: clonePending(Pending{Scope: scope, Request: request}), binding: binding, done: make(chan struct{}),
	}
	partition.pending[key] = call
	partition.pendingBytes += size
	hub.pendingCount++
	hub.pendingBytes += size
	binding.active++
	hub.publishLocked(partition, DeltaOpened, call.value)
	hub.mu.Unlock()

	select {
	case <-call.done:
		return cloneResponse(call.result.response), call.result.err
	case <-ctx.Done():
		result, completed := hub.complete(key, call, pendingResult{err: ctx.Err()})
		if !completed {
			<-call.done
		}
		return cloneResponse(result.response), result.err
	}
}

func (hub *PendingHub) hasPendingCapacity(partition *pendingPartition, size int) bool {
	return hub.pendingCount < hub.limits.Pending && len(partition.pending) < hub.limits.PendingPerClient &&
		size <= hub.limits.PendingBytes && hub.pendingBytes <= hub.limits.PendingBytes-size &&
		size <= hub.limits.PendingBytesPerClient && partition.pendingBytes <= hub.limits.PendingBytesPerClient-size
}

// Respond completes only an interaction owned by clientID. A wrong client
// cannot discover or answer another client's request.
func (hub *PendingHub) Respond(clientID string, scope interaction.Scope, response interaction.Response) error {
	if hub == nil {
		return ErrPendingHubClosed
	}
	if err := boundedToken("client ID", clientID); err != nil {
		return err
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	if err := response.Validate(); err != nil {
		return err
	}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return ErrPendingHubClosed
	}
	binding := hub.runs[scope.RunID()]
	if binding == nil || binding.clientID != clientID {
		hub.mu.Unlock()
		return ErrRunNotBound
	}
	partition := hub.partitions[clientID]
	if partition == nil {
		hub.mu.Unlock()
		return ErrRunNotBound
	}
	key := makePendingKey(scope, response.ID())
	call := partition.pending[key]
	if call == nil {
		hub.mu.Unlock()
		return ErrInteractionNotPending
	}
	hub.finishCallLocked(partition, key, call, pendingResult{response: response.Clone()})
	hub.mu.Unlock()
	close(call.done)
	return nil
}

func (hub *PendingHub) complete(key pendingKey, call *pendingCall, proposed pendingResult) (pendingResult, bool) {
	hub.mu.Lock()
	partition := hub.partitions[call.binding.clientID]
	if partition != nil && partition.pending[key] == call {
		hub.finishCallLocked(partition, key, call, proposed)
		hub.mu.Unlock()
		close(call.done)
		return proposed, true
	}
	result := call.result
	hub.mu.Unlock()
	return result, false
}

func (hub *PendingHub) finishCallLocked(partition *pendingPartition, key pendingKey, call *pendingCall, result pendingResult) {
	delete(partition.pending, key)
	size := pendingValueBytes(call.value)
	partition.pendingBytes -= size
	hub.pendingCount--
	hub.pendingBytes -= size
	call.result = result
	call.binding.active--
	hub.publishLocked(partition, DeltaClosed, call.value)
	hub.removeReleasedBindingLocked(call.binding)
}

func (hub *PendingHub) removeReleasedBindingLocked(binding *runBindingState) {
	if binding == nil || binding.accepting || binding.active != 0 {
		return
	}
	if hub.runs[binding.runID] == binding {
		delete(hub.runs, binding.runID)
		partition := hub.partitions[binding.clientID]
		if partition != nil {
			partition.boundRuns--
		}
		close(binding.released)
	}
}

// Snapshot returns one stable client's complete sorted pending view without
// allocating an observer or consuming any queue reservation. A client without
// retained interaction state receives the initial empty revision.
func (hub *PendingHub) Snapshot(clientID string) (PendingSnapshot, error) {
	if hub == nil {
		return PendingSnapshot{}, ErrPendingHubClosed
	}
	if err := boundedToken("client ID", clientID); err != nil {
		return PendingSnapshot{}, err
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return PendingSnapshot{}, ErrPendingHubClosed
	}
	partition := hub.partitions[clientID]
	if partition == nil {
		return PendingSnapshot{}, nil
	}
	return hub.snapshotLocked(partition), nil
}

// Subscribe atomically captures one stable client's complete sorted pending
// set and registers its tail before releasing the hub lock.
func (hub *PendingHub) Subscribe(ctx context.Context, clientID string) (*PendingSubscription, error) {
	if hub == nil {
		return nil, ErrPendingHubClosed
	}
	if ctx == nil {
		return nil, errors.New("pending subscription context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := boundedToken("client ID", clientID); err != nil {
		return nil, err
	}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return nil, ErrPendingHubClosed
	}
	partition, err := hub.partitionLocked(clientID, true)
	if err != nil {
		hub.mu.Unlock()
		if errors.Is(err, errPendingClientCapacity) {
			return nil, fmt.Errorf("%w: client partitions", ErrObserverCapacity)
		}
		return nil, err
	}
	if partition == nil {
		hub.mu.Unlock()
		return nil, fmt.Errorf("%w: client partition unavailable", ErrObserverCapacity)
	}
	if hub.observerCount >= hub.limits.Observers || len(partition.watchers) >= hub.limits.ObserversPerClient {
		hub.mu.Unlock()
		return nil, ErrObserverCapacity
	}
	if partition.nextWatcher == math.MaxUint64 {
		hub.mu.Unlock()
		return nil, fmt.Errorf("%w: identity exhausted", ErrObserverCapacity)
	}
	if hub.reservedEntries > hub.limits.ReservedQueueEntries-hub.limits.ObserverQueueEntries ||
		partition.reservedEntries > hub.limits.ReservedQueueEntriesPerClient-hub.limits.ObserverQueueEntries ||
		hub.reservedBytes > hub.limits.ReservedQueueBytes-hub.limits.ObserverQueueBytes ||
		partition.reservedBytes > hub.limits.ReservedQueueBytesPerClient-hub.limits.ObserverQueueBytes {
		hub.mu.Unlock()
		return nil, fmt.Errorf("%w: queue reservation", ErrObserverCapacity)
	}
	snapshot := hub.snapshotLocked(partition)
	partition.nextWatcher++
	id := partition.nextWatcher
	watcher := &pendingWatcher{
		hub: hub, partition: partition, id: id,
		queue: make(chan queuedDelta, hub.limits.ObserverQueueEntries), out: make(chan Delta),
		stop: make(chan struct{}), done: make(chan struct{}), active: true,
		lastDelivered: snapshot.Revision,
	}
	partition.watchers[id] = watcher
	hub.observerCount++
	hub.reservedEntries += hub.limits.ObserverQueueEntries
	partition.reservedEntries += hub.limits.ObserverQueueEntries
	hub.reservedBytes += hub.limits.ObserverQueueBytes
	partition.reservedBytes += hub.limits.ObserverQueueBytes
	subscription := &PendingSubscription{hub: hub, snapshot: snapshot, watcher: watcher}
	hub.watcherWG.Add(1)
	hub.mu.Unlock()

	go watcher.deliver()
	stopContext := context.AfterFunc(ctx, func() { hub.detachWatcher(watcher, ctx.Err(), false) })
	hub.mu.Lock()
	if watcher.active {
		watcher.stopContext = stopContext
	} else {
		stopContext()
	}
	hub.mu.Unlock()
	return subscription, nil
}

func (hub *PendingHub) snapshotLocked(partition *pendingPartition) PendingSnapshot {
	values := make([]Pending, 0, len(partition.pending))
	for _, call := range partition.pending {
		values = append(values, clonePending(call.value))
	}
	sort.Slice(values, func(first, second int) bool {
		firstRun, secondRun := values[first].Scope.RunID(), values[second].Scope.RunID()
		if firstRun != secondRun {
			return firstRun < secondRun
		}
		return values[first].Request.ID() < values[second].Request.ID()
	})
	return PendingSnapshot{Revision: partition.revision, Pending: values}
}

func (hub *PendingHub) publishLocked(partition *pendingPartition, kind DeltaKind, pending Pending) {
	partition.revision++
	delta := Delta{Revision: partition.revision, Kind: kind, Pending: clonePending(pending)}
	ids := sortedWatcherIDs(partition.watchers)
	for _, id := range ids {
		watcher := partition.watchers[id]
		if watcher == nil {
			continue
		}
		if exhausted := hub.enqueueLocked(watcher, delta); exhausted != nil {
			hub.finishWatcherLocked(watcher, exhausted, true)
		}
	}
}

func (hub *PendingHub) enqueueLocked(watcher *pendingWatcher, delta Delta) *ObserverExhaustedError {
	if !watcher.active {
		return nil
	}
	if watcher.queuedCount >= hub.limits.ObserverQueueEntries {
		return newObserverExhausted(
			"pending_observer_queue_entries", hub.limits.ObserverQueueEntries, watcher.queuedCount+1,
		)
	}
	size := pendingDeltaBytes(delta)
	if size > hub.limits.ObserverQueueBytes || watcher.queuedBytes > hub.limits.ObserverQueueBytes-size {
		return newObserverExhausted(
			"pending_observer_queue_bytes", hub.limits.ObserverQueueBytes, watcher.queuedBytes+size,
		)
	}
	if hub.queuedEntries >= hub.limits.QueuedEntries {
		return newObserverExhausted("pending_observer_entries", hub.limits.QueuedEntries, hub.queuedEntries+1)
	}
	if watcher.partition.queuedEntries >= hub.limits.QueuedEntriesPerClient {
		return newObserverExhausted(
			"pending_observer_client_entries",
			hub.limits.QueuedEntriesPerClient,
			watcher.partition.queuedEntries+1,
		)
	}
	if size > hub.limits.QueuedBytes || hub.queuedBytes > hub.limits.QueuedBytes-size {
		return newObserverExhausted("pending_observer_bytes", hub.limits.QueuedBytes, hub.queuedBytes+size)
	}
	if size > hub.limits.QueuedBytesPerClient ||
		watcher.partition.queuedBytes > hub.limits.QueuedBytesPerClient-size {
		return newObserverExhausted(
			"pending_observer_client_bytes",
			hub.limits.QueuedBytesPerClient,
			watcher.partition.queuedBytes+size,
		)
	}
	item := queuedDelta{delta: cloneDelta(delta), bytes: size}
	select {
	case watcher.queue <- item:
		watcher.queuedCount++
		watcher.queuedBytes += size
		watcher.partition.queuedEntries++
		watcher.partition.queuedBytes += size
		hub.queuedEntries++
		hub.queuedBytes += size
		return nil
	default:
		return newObserverExhausted(
			"pending_observer_queue_entries", cap(watcher.queue), len(watcher.queue)+1,
		)
	}
}

func (watcher *pendingWatcher) deliver() {
	defer func() {
		watcher.discardQueued()
		close(watcher.out)
		close(watcher.done)
		watcher.hub.watcherStopped(watcher)
		watcher.hub.watcherWG.Done()
	}()
	for {
		select {
		case <-watcher.stop:
			return
		case item := <-watcher.queue:
			select {
			case <-watcher.stop:
				return
			case watcher.out <- cloneDelta(item.delta):
				watcher.hub.delivered(watcher, item)
			}
		}
	}
}

func (watcher *pendingWatcher) discardQueued() {
	for {
		select {
		case <-watcher.queue:
		default:
			return
		}
	}
}

func (hub *PendingHub) delivered(watcher *pendingWatcher, item queuedDelta) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if item.delta.Revision > watcher.lastDelivered {
		watcher.lastDelivered = item.delta.Revision
	}
	if !watcher.active {
		return
	}
	watcher.queuedCount--
	watcher.queuedBytes -= item.bytes
	watcher.partition.queuedEntries--
	watcher.partition.queuedBytes -= item.bytes
	hub.queuedEntries--
	hub.queuedBytes -= item.bytes
}

func (hub *PendingHub) detachWatcher(watcher *pendingWatcher, err error, exhausted bool) {
	hub.mu.Lock()
	hub.finishWatcherLocked(watcher, err, exhausted)
	hub.mu.Unlock()
}

func (hub *PendingHub) finishWatcherLocked(watcher *pendingWatcher, err error, exhausted bool) {
	if watcher == nil || !watcher.active {
		return
	}
	watcher.active = false
	watcher.err = err
	watcher.exhausted = exhausted
	delete(watcher.partition.watchers, watcher.id)
	watcher.partition.stopping[watcher] = struct{}{}
	hub.observerCount--
	hub.queuedEntries -= watcher.queuedCount
	hub.queuedBytes -= watcher.queuedBytes
	watcher.partition.queuedEntries -= watcher.queuedCount
	watcher.partition.queuedBytes -= watcher.queuedBytes
	watcher.queuedBytes = 0
	watcher.queuedCount = 0
	hub.reservedEntries -= hub.limits.ObserverQueueEntries
	watcher.partition.reservedEntries -= hub.limits.ObserverQueueEntries
	hub.reservedBytes -= hub.limits.ObserverQueueBytes
	watcher.partition.reservedBytes -= hub.limits.ObserverQueueBytes
	if watcher.stopContext != nil {
		watcher.stopContext()
		watcher.stopContext = nil
	}
	close(watcher.stop)
}

func (hub *PendingHub) watcherStopped(watcher *pendingWatcher) {
	hub.mu.Lock()
	delete(watcher.partition.stopping, watcher)
	hub.mu.Unlock()
}

// FenceObservers immediately terminates a stable client's current discovery
// streams. It preserves that client's revision and pending calls for a new
// complete-first subscription after reconnect.
func (hub *PendingHub) FenceObservers(clientID string) error {
	if hub == nil {
		return ErrPendingHubClosed
	}
	if err := boundedToken("client ID", clientID); err != nil {
		return err
	}
	hub.mu.Lock()
	if hub.closed {
		done := hub.closeDone
		hub.mu.Unlock()
		<-done
		return ErrPendingHubClosed
	}
	partition := hub.partitions[clientID]
	if partition == nil {
		hub.mu.Unlock()
		return nil
	}
	watchers := make([]*pendingWatcher, 0, len(partition.watchers)+len(partition.stopping))
	for watcher := range partition.stopping {
		watchers = append(watchers, watcher)
	}
	for _, id := range sortedWatcherIDs(partition.watchers) {
		watcher := partition.watchers[id]
		watchers = append(watchers, watcher)
		hub.finishWatcherLocked(watcher, ErrObserverFenced, false)
	}
	sort.Slice(watchers, func(first, second int) bool { return watchers[first].id < watchers[second].id })
	hub.mu.Unlock()
	for _, watcher := range watchers {
		<-watcher.done
	}
	return nil
}

func sortedWatcherIDs(watchers map[uint64]*pendingWatcher) []uint64 {
	ids := make([]uint64, 0, len(watchers))
	for id := range watchers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Close releases pending callers, immediately stops every observer without
// waiting for clients to read queued deltas, and joins their delivery loops.
func (hub *PendingHub) Close() {
	if hub == nil {
		return
	}
	hub.mu.Lock()
	if hub.closed {
		done := hub.closeDone
		hub.mu.Unlock()
		<-done
		return
	}
	hub.closed = true
	bindings := make([]*runBindingState, 0, len(hub.runs))
	for _, binding := range hub.runs {
		bindings = append(bindings, binding)
	}
	sort.Slice(bindings, func(first, second int) bool {
		if bindings[first].clientID != bindings[second].clientID {
			return bindings[first].clientID < bindings[second].clientID
		}
		return bindings[first].runID < bindings[second].runID
	})
	for _, binding := range bindings {
		binding.accepting = false
	}
	type keyedCall struct {
		clientID string
		key      pendingKey
		call     *pendingCall
	}
	calls := make([]keyedCall, 0, hub.pendingCount)
	for clientID, partition := range hub.partitions {
		for key, call := range partition.pending {
			calls = append(calls, keyedCall{clientID: clientID, key: key, call: call})
		}
	}
	sort.Slice(calls, func(first, second int) bool {
		if calls[first].clientID != calls[second].clientID {
			return calls[first].clientID < calls[second].clientID
		}
		if calls[first].key.runID != calls[second].key.runID {
			return calls[first].key.runID < calls[second].key.runID
		}
		return calls[first].key.interactionID < calls[second].key.interactionID
	})
	for _, value := range calls {
		partition := hub.partitions[value.clientID]
		if partition == nil {
			value.call.result = pendingResult{err: ErrPendingHubClosed}
			value.call.binding.active--
			continue
		}
		hub.finishCallLocked(partition, value.key, value.call, pendingResult{err: ErrPendingHubClosed})
	}
	for _, binding := range bindings {
		hub.removeReleasedBindingLocked(binding)
	}
	clientIDs := make([]string, 0, len(hub.partitions))
	for clientID := range hub.partitions {
		clientIDs = append(clientIDs, clientID)
	}
	sort.Strings(clientIDs)
	for _, clientID := range clientIDs {
		partition := hub.partitions[clientID]
		if partition == nil {
			continue
		}
		for _, id := range sortedWatcherIDs(partition.watchers) {
			hub.finishWatcherLocked(partition.watchers[id], ErrPendingHubClosed, false)
		}
	}
	hub.mu.Unlock()
	for _, value := range calls {
		close(value.call.done)
	}
	hub.watcherWG.Wait()
	close(hub.closeDone)
}

func clonePending(value Pending) Pending {
	return Pending{Scope: value.Scope, Request: value.Request.Clone()}
}

func clonePendingSnapshot(value PendingSnapshot) PendingSnapshot {
	result := PendingSnapshot{Revision: value.Revision, Pending: make([]Pending, len(value.Pending))}
	for index, pending := range value.Pending {
		result.Pending[index] = clonePending(pending)
	}
	return result
}

func cloneDelta(value Delta) Delta {
	return Delta{Revision: value.Revision, Kind: value.Kind, Pending: clonePending(value.Pending)}
}

func cloneResponse(value interaction.Response) interaction.Response {
	if value.Validate() != nil {
		return interaction.Response{}
	}
	return value.Clone()
}

func pendingDeltaBytes(value Delta) int {
	return pendingValueBytes(value.Pending) + 32
}

func pendingValueBytes(value Pending) int {
	return len(value.Scope.RunID()) + len(value.Request.ID()) +
		len(value.Request.Kind()) + len(value.Request.Prompt()) +
		len(value.Request.Schema())
}
