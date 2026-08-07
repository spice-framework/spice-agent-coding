package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/spice-framework/spice-agent/event"
	"github.com/spice-framework/spice-agent/interaction"
	"github.com/spice-framework/spice-agent/message"
	"github.com/spice-framework/spice-agent/model"
	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
)

const defaultFinalizationTimeout = 2 * time.Second

// EngineOptions configures authoritative replay and bounded terminal durability.
type EngineOptions struct {
	LogLimits           event.LogLimits
	FinalizationTimeout time.Duration
	// MetadataNamespaces explicitly permits safe provider metadata in events.
	MetadataNamespaces []string
	// CompiledPlanIdentities names every executable generated provider, stage,
	// observer, broker, static tool, and dispatcher decorator bean. Generated
	// callers include module version and source identity in each value.
	CompiledPlanIdentities []string
	// SnapshotCompatibilityIdentity is an explicit compiler-generated semantic
	// identity for portable snapshot import. Empty disables Engine.ResumeSnapshot.
	SnapshotCompatibilityIdentity string
}

// DefaultEngineOptions returns conservative architecture-proof bounds.
func DefaultEngineOptions() EngineOptions {
	return EngineOptions{
		LogLimits: event.DefaultLogLimits(), FinalizationTimeout: defaultFinalizationTimeout,
		CompiledPlanIdentities: []string{"broker:injected", "provider:injected", "stage:kernel"},
	}
}

// DurabilityError reports that a lifecycle event could not be fully persisted
// or acknowledged before its bounded finalization deadline.
type DurabilityError struct {
	Kind  event.Kind
	Cause error
}

func (failure *DurabilityError) Error() string {
	return fmt.Sprintf("durably persist %s: %v", failure.Kind, failure.Cause)
}

func (failure *DurabilityError) Unwrap() error { return failure.Cause }

// EmissionError reports a post-commit observer acknowledgement failure. The
// sequence is already authoritative and must never be reused.
type EmissionError struct {
	Kind      event.Kind
	Sequence  uint64
	Committed bool
	Cause     error
}

func (failure *EmissionError) Error() string {
	return fmt.Sprintf("publish committed %s sequence %d: %v", failure.Kind, failure.Sequence, failure.Cause)
}

func (failure *EmissionError) Unwrap() error { return failure.Cause }

func committed(err error) bool {
	if err == nil {
		return true
	}
	var emission *EmissionError
	return errors.As(err, &emission) && emission.Committed
}

// IDSource supplies stable operation identities. Applications may replace the
// default through ordinary typed Spice injection.
type IDSource interface {
	Next(prefix string) (string, error)
}

// AtomicIDSource is a deterministic process-local ID source for embedding and tests.
type AtomicIDSource struct{ next atomic.Uint64 }

// Next returns prefix-N, starting at one.
func (source *AtomicIDSource) Next(prefix string) (string, error) {
	if source == nil {
		return "", errors.New("agent ID source is nil")
	}
	if prefix == "" || prefix != strings.TrimSpace(prefix) {
		return "", errors.New("agent ID prefix must be non-empty without surrounding whitespace")
	}
	return prefix + "-" + strconv.FormatUint(source.next.Add(1), 10), nil
}

// Definition is immutable per-run behavior selected before execution begins.
type Definition struct {
	name     string
	model    string
	maxTurns uint32
}

// NewDefinition constructs one run definition.
func NewDefinition(name, modelName string, maxTurns uint32) (Definition, error) {
	if name == "" || name != strings.TrimSpace(name) {
		return Definition{}, errors.New("agent definition name must be non-empty without surrounding whitespace")
	}
	if modelName == "" || modelName != strings.TrimSpace(modelName) {
		return Definition{}, errors.New("agent model name must be non-empty without surrounding whitespace")
	}
	if maxTurns == 0 || maxTurns > 1000 {
		return Definition{}, errors.New("agent maximum turns must be between 1 and 1000")
	}
	return Definition{name: name, model: modelName, maxTurns: maxTurns}, nil
}

func (definition Definition) Name() string     { return definition.name }
func (definition Definition) Model() string    { return definition.model }
func (definition Definition) MaxTurns() uint32 { return definition.maxTurns }

// Input is immutable initial run input.
type Input struct{ message message.Message }

// NewInput validates an initial user message.
func NewInput(initial message.Message) (Input, error) {
	if err := initial.Validate(); err != nil {
		return Input{}, fmt.Errorf("agent input message: %w", err)
	}
	if initial.Role() != message.RoleUser {
		return Input{}, errors.New("agent input initial message must have user role")
	}
	return Input{message: initial.Clone()}, nil
}

// Engine executes an already-constructed Spice graph. It is not a container.
// Close drains cooperative runs; Shutdown cancels them. Neither method can
// forcibly stop a trusted in-process provider or tool that ignores context.
type Engine struct {
	provider              model.Provider
	toolPlans             stage.ToolPlanSource
	ids                   IDSource
	clock                 func() time.Time
	observers             []event.Observer
	bestEffort            []*event.BestEffortObserver
	logLimits             event.LogLimits
	finalizationTimeout   time.Duration
	metadataNamespaces    map[string]struct{}
	broker                interaction.Broker
	compiledPlan          []string
	snapshotCompatibility string

	mu      sync.Mutex
	closed  bool
	active  map[string]*Run
	seen    map[string]struct{}
	drained chan struct{}
}

// NewEngine constructs the kernel with default bounded replay limits.
func NewEngine(provider model.Provider, dispatcher stage.ToolDispatcher, ids IDSource, clock func() time.Time, observers []event.Observer, bestEffort []*event.BestEffortObserver) (*Engine, error) {
	return NewEngineWithOptions(provider, dispatcher, ids, clock, observers, bestEffort, DefaultEngineOptions())
}

// NewEngineWithLimits constructs the kernel with explicit replay bounds.
func NewEngineWithLimits(provider model.Provider, dispatcher stage.ToolDispatcher, ids IDSource, clock func() time.Time, observers []event.Observer, bestEffort []*event.BestEffortObserver, limits event.LogLimits) (*Engine, error) {
	options := DefaultEngineOptions()
	options.LogLimits = limits
	return NewEngineWithOptions(provider, dispatcher, ids, clock, observers, bestEffort, options)
}

// NewEngineWithOptions constructs the kernel with explicit bounded options.
func NewEngineWithOptions(provider model.Provider, dispatcher stage.ToolDispatcher, ids IDSource, clock func() time.Time, observers []event.Observer, bestEffort []*event.BestEffortObserver, options EngineOptions) (*Engine, error) {
	return NewEngineWithInteractionBroker(provider, dispatcher, interaction.UnavailableBroker{}, ids, clock, observers, bestEffort, options)
}

// NewEngineWithToolPlanSource constructs an engine whose future runs lease the
// source's current immutable tool generation.
func NewEngineWithToolPlanSource(
	provider model.Provider,
	toolPlans stage.ToolPlanSource,
	ids IDSource,
	clock func() time.Time,
	observers []event.Observer,
	bestEffort []*event.BestEffortObserver,
	options EngineOptions,
) (*Engine, error) {
	return NewEngineWithToolPlanSourceAndInteractionBroker(
		provider, toolPlans, interaction.UnavailableBroker{}, ids, clock, observers, bestEffort, options,
	)
}

// NewEngineWithInteractionBroker constructs an engine with a UI-neutral broker.
func NewEngineWithInteractionBroker(provider model.Provider, dispatcher stage.ToolDispatcher, broker interaction.Broker, ids IDSource, clock func() time.Time, observers []event.Observer, bestEffort []*event.BestEffortObserver, options EngineOptions) (*Engine, error) {
	if dispatcher == nil {
		return nil, errors.New("agent engine requires a tool dispatcher")
	}
	toolPlans, err := stage.NewStaticToolPlanSource(dispatcher)
	if err != nil {
		return nil, fmt.Errorf("agent static tool plan: %w", err)
	}
	return NewEngineWithToolPlanSourceAndInteractionBroker(
		provider, toolPlans, broker, ids, clock, observers, bestEffort, options,
	)
}

// NewEngineWithToolPlanSourceAndInteractionBroker constructs the full kernel
// boundary with a per-run tool plan source and UI-neutral broker.
func NewEngineWithToolPlanSourceAndInteractionBroker(
	provider model.Provider,
	toolPlans stage.ToolPlanSource,
	broker interaction.Broker,
	ids IDSource,
	clock func() time.Time,
	observers []event.Observer,
	bestEffort []*event.BestEffortObserver,
	options EngineOptions,
) (*Engine, error) {
	if provider == nil {
		return nil, errors.New("agent engine requires a model provider")
	}
	if toolPlans == nil {
		return nil, errors.New("agent engine requires a tool plan source")
	}
	if broker == nil {
		return nil, errors.New("agent engine requires an interaction broker")
	}
	if ids == nil {
		return nil, errors.New("agent engine requires an ID source")
	}
	if clock == nil {
		return nil, errors.New("agent engine requires a clock")
	}
	for index, observer := range observers {
		if observer == nil {
			return nil, fmt.Errorf("agent observer %d is nil", index)
		}
	}
	for index, observer := range bestEffort {
		if observer == nil {
			return nil, fmt.Errorf("agent best-effort observer %d is nil", index)
		}
	}
	if options.FinalizationTimeout <= 0 || options.FinalizationTimeout > 30*time.Second {
		return nil, errors.New("agent finalization timeout must be between zero and 30 seconds")
	}
	metadataNamespaces := make(map[string]struct{}, len(options.MetadataNamespaces))
	for index, namespace := range options.MetadataNamespaces {
		if namespace == "" || namespace != strings.TrimSpace(namespace) || len(namespace) > 128 {
			return nil, fmt.Errorf("agent metadata namespace %d is invalid", index)
		}
		if _, duplicate := metadataNamespaces[namespace]; duplicate {
			return nil, fmt.Errorf("agent metadata namespace %q is duplicated", namespace)
		}
		metadataNamespaces[namespace] = struct{}{}
	}
	compiledPlan, err := buildCompiledPlan(options.CompiledPlanIdentities)
	if err != nil {
		return nil, err
	}
	if err = validateSnapshotCompatibilityIdentity(options.SnapshotCompatibilityIdentity); err != nil {
		return nil, err
	}
	probe, err := event.NewLog("validation", options.LogLimits)
	if err != nil {
		return nil, fmt.Errorf("agent replay limits: %w", err)
	}
	probe.Close()
	drained := make(chan struct{})
	close(drained)
	return &Engine{
		provider: provider, toolPlans: toolPlans, ids: ids, clock: clock,
		observers: append([]event.Observer(nil), observers...), bestEffort: append([]*event.BestEffortObserver(nil), bestEffort...),
		logLimits: options.LogLimits, finalizationTimeout: options.FinalizationTimeout,
		metadataNamespaces:    metadataNamespaces,
		broker:                broker,
		compiledPlan:          compiledPlan,
		snapshotCompatibility: options.SnapshotCompatibilityIdentity,
		active:                make(map[string]*Run), seen: make(map[string]struct{}), drained: drained,
	}, nil
}

// Run is one asynchronous execution backed by an authoritative replay log.
type Run struct {
	id     string
	log    *event.Log
	done   chan struct{}
	cancel context.CancelFunc
	engine *Engine
	// The run owns this derived context and cancels it during finalization.
	//nolint:containedctx // required to link concurrent interaction lifecycles to run cancellation
	ctx      context.Context
	emitter  *runEmitter
	finalize sync.Once
	mu       sync.Mutex
	err      error

	stateMu            sync.Mutex
	definition         Definition
	dispatcher         stage.ToolDispatcher
	planLease          *stage.ToolPlanLease
	planIdentity       PlanIdentity
	history            []message.Message
	completedTurns     uint32
	status             LifecycleStatus
	started            bool
	lastSequence       uint64
	suspendRequested   bool
	suspendWaiter      chan error
	resumeSignal       chan struct{}
	localResume        *PreparedLocalResume
	activeInteractions map[interaction.ID]struct{}
	seenInteractions   map[interaction.ID]struct{}
	interactionWG      sync.WaitGroup
	messageIDs         map[message.ID]struct{}
}

func (run *Run) ID() string { return run.id }

// PlanIdentity returns the immutable compiled and leased execution identity.
func (run *Run) PlanIdentity() PlanIdentity {
	if run == nil {
		return PlanIdentity{}
	}
	return run.planIdentity.clone()
}

// ToolPlanID returns the exact leased tool generation.
func (run *Run) ToolPlanID() stage.PlanID {
	if run == nil {
		return ""
	}
	return run.planIdentity.ToolPlanID()
}

// Subscribe creates an independent gap-free replay/tail cursor.
func (run *Run) Subscribe(ctx context.Context, afterSequence uint64) (*event.Subscription, error) {
	if run == nil {
		return nil, errors.New("agent run is nil")
	}
	return run.log.Subscribe(ctx, afterSequence)
}

// ReplayEvents captures one bounded authoritative replay page and may
// atomically register a live tail when the page reaches the captured head.
func (run *Run) ReplayEvents(ctx context.Context, request event.ReplayRequest) (event.ReplayPage, error) {
	if run == nil {
		return event.ReplayPage{}, errors.New("agent run is nil")
	}
	return run.log.Replay(ctx, request)
}

// Cancel requests cooperative run cancellation.
func (run *Run) Cancel() {
	if run != nil {
		run.cancel()
	}
}

// Wait blocks for terminal persistence and returns the normalized run error.
func (run *Run) Wait(ctx context.Context) error {
	if ctx == nil {
		return errors.New("agent wait context must not be nil")
	}
	if run == nil {
		return errors.New("agent run is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-run.done:
		run.mu.Lock()
		defer run.mu.Unlock()
		return run.err
	}
}

const (
	runStatusRunning    LifecycleStatus = "running"
	runStatusSuspending LifecycleStatus = "suspending"
	runStatusFinishing  LifecycleStatus = "finishing"
)

// UnsafeSnapshotError reports state that cannot be exported or resumed safely.
type UnsafeSnapshotError struct {
	Status             LifecycleStatus
	ActiveInteractions int
}

func (failure *UnsafeSnapshotError) Error() string {
	return fmt.Sprintf("agent snapshot boundary is unsafe: status=%s active_interactions=%d", failure.Status, failure.ActiveInteractions)
}

// Suspend requests a pause after the current fully finalized turn. Active or
// uncertain tool/interaction operations are never captured.
func (run *Run) Suspend(ctx context.Context) error {
	if ctx == nil {
		return errors.New("agent suspend context must not be nil")
	}
	if run == nil {
		return errors.New("agent run is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	run.stateMu.Lock()
	if run.status != runStatusRunning || !run.started || run.suspendRequested {
		failure := &UnsafeSnapshotError{Status: run.status, ActiveInteractions: len(run.activeInteractions)}
		run.stateMu.Unlock()
		return failure
	}
	waiter := make(chan error, 1)
	run.suspendRequested = true
	run.suspendWaiter = waiter
	run.status = runStatusSuspending
	run.stateMu.Unlock()
	select {
	case <-ctx.Done():
		run.stateMu.Lock()
		if run.suspendRequested && run.suspendWaiter == waiter {
			run.suspendRequested = false
			run.suspendWaiter = nil
			run.status = runStatusRunning
		} else if run.status == LifecycleSuspended && run.resumeSignal != nil {
			_ = run.resumeLocked()
		}
		run.stateMu.Unlock()
		return ctx.Err()
	case err := <-waiter:
		return err
	}
}

// Resume continues a locally suspended run using its immutable plan snapshot.
func (run *Run) Resume() error {
	prepared, err := run.PrepareLocalResume()
	if err != nil {
		return err
	}
	return prepared.Commit()
}

func (run *Run) resumeLocked() error {
	if run.status != LifecycleSuspended || run.resumeSignal == nil {
		return &UnsafeSnapshotError{Status: run.status, ActiveInteractions: len(run.activeInteractions)}
	}
	resumeSignal := run.resumeSignal
	// Make snapshot export fail before unblocking execution. Otherwise the
	// caller can duplicate suspended authority between Resume returning and the
	// execution goroutine observing resumeSignal.
	run.status = runStatusRunning
	run.resumeSignal = nil
	close(resumeSignal)
	return nil
}

// ExportSnapshot returns state only at a suspended or terminal safe boundary.
func (run *Run) ExportSnapshot() (Snapshot, error) {
	if run == nil {
		return Snapshot{}, errors.New("agent run is nil")
	}
	run.stateMu.Lock()
	defer run.stateMu.Unlock()
	if run.status != LifecycleSuspended && run.status != LifecycleCompleted && run.status != LifecycleFailed && run.status != LifecycleCancelled {
		return Snapshot{}, &UnsafeSnapshotError{Status: run.status, ActiveInteractions: len(run.activeInteractions)}
	}
	if len(run.activeInteractions) != 0 {
		return Snapshot{}, &UnsafeSnapshotError{Status: run.status, ActiveInteractions: len(run.activeInteractions)}
	}
	return newSnapshot(
		run.id, run.definition, run.completedTurns, run.history, run.planIdentity,
		slices.Sorted(maps.Keys(run.seenInteractions)),
		run.lastSequence, run.status,
	)
}

// Interact executes one UI-neutral interaction through the injected broker.
func (run *Run) Interact(ctx context.Context, request interaction.Request) (interaction.Response, error) {
	if ctx == nil {
		return interaction.Response{}, errors.New("agent interaction context must not be nil")
	}
	if run == nil {
		return interaction.Response{}, errors.New("agent run is nil")
	}
	if err := request.Validate(); err != nil {
		return interaction.Response{}, err
	}
	if err := ctx.Err(); err != nil {
		return interaction.Response{}, err
	}
	if err := run.reserveInteraction(request.ID()); err != nil {
		return interaction.Response{}, err
	}
	defer run.releaseInteraction(request.ID())
	if err := run.emitter.emit(ctx, event.InteractionStarted, map[string]string{"id": string(request.ID()), "kind": request.Kind()}); err != nil {
		if committed(err) {
			return interaction.Response{}, errors.Join(err, run.emitter.interactionFailure(ctx, event.InteractionFailed, request.ID()))
		}
		return interaction.Response{}, err
	}
	brokerContext, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(run.ctx, cancel)
	scope, scopeErr := interaction.NewScope(run.id)
	if scopeErr != nil {
		stop()
		cancel()
		return interaction.Response{}, errors.Join(scopeErr, run.emitter.interactionFailure(ctx, event.InteractionFailed, request.ID()))
	}
	response, err := safeInteraction(brokerContext, run.engine.broker, scope, request.Clone())
	stop()
	cancel()
	if err != nil {
		kind := run.interactionFailureKind(ctx)
		return interaction.Response{}, errors.Join(err, run.emitter.interactionFailure(ctx, kind, request.ID()))
	}
	if err = response.Validate(); err != nil {
		return interaction.Response{}, errors.Join(err, run.emitter.interactionFailure(ctx, event.InteractionFailed, request.ID()))
	}
	if response.ID() != request.ID() {
		err = fmt.Errorf("interaction response ID %q does not match request %q", response.ID(), request.ID())
		return interaction.Response{}, errors.Join(err, run.emitter.interactionFailure(ctx, event.InteractionFailed, request.ID()))
	}
	if err = run.emitter.emit(ctx, event.InteractionCompleted, map[string]string{"id": string(response.ID())}); err != nil {
		if !committed(err) {
			kind := run.interactionFailureKind(ctx)
			return interaction.Response{}, errors.Join(err, run.emitter.interactionFailure(ctx, kind, request.ID()))
		}
		return interaction.Response{}, err
	}
	return response.Clone(), nil
}

func (run *Run) interactionFailureKind(ctx context.Context) event.Kind {
	if run.ctx.Err() != nil || ctx.Err() != nil {
		return event.InteractionCancelled
	}
	return event.InteractionFailed
}

func (run *Run) reserveInteraction(id interaction.ID) error {
	run.stateMu.Lock()
	defer run.stateMu.Unlock()
	if run.status != runStatusRunning || !run.started {
		return &UnsafeSnapshotError{Status: run.status, ActiveInteractions: len(run.activeInteractions)}
	}
	if _, duplicate := run.activeInteractions[id]; duplicate {
		return fmt.Errorf("interaction ID %q is already active", id)
	}
	if _, duplicate := run.seenInteractions[id]; duplicate {
		return fmt.Errorf("interaction ID %q was already used by this run", id)
	}
	run.seenInteractions[id] = struct{}{}
	run.activeInteractions[id] = struct{}{}
	run.interactionWG.Add(1)
	return nil
}

func (run *Run) releaseInteraction(id interaction.ID) {
	run.stateMu.Lock()
	delete(run.activeInteractions, id)
	run.stateMu.Unlock()
	run.interactionWG.Done()
}

// Start begins one run. Caller context ownership propagates to providers and tools.
func (engine *Engine) Start(ctx context.Context, definition Definition, input Input) (*Run, error) {
	prepared, err := engine.PrepareStart(ctx, definition, input)
	if err != nil {
		return nil, err
	}
	return prepared.Commit(ctx)
}

func (engine *Engine) isClosed() bool {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.closed
}

func buildCompiledPlan(configured []string) ([]string, error) {
	result := append([]string(nil), configured...)
	slices.Sort(result)
	if err := validateCompiledPlan(result); err != nil {
		return nil, err
	}
	return result, nil
}

func leaseCurrentToolPlan(
	ctx context.Context,
	source stage.ToolPlanSource,
	releaseTimeout time.Duration,
) (*stage.ToolPlanLease, error) {
	lease, err := safeLeaseCurrent(ctx, source)
	lease, err = validateAcquiredLease(lease, err, "current", releaseTimeout)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, releaseLeaseOnRollback(lease, err, releaseTimeout)
	}
	return lease, nil
}

func leaseToolPlanGeneration(
	ctx context.Context,
	source stage.ToolPlanSource,
	id stage.PlanID,
	releaseTimeout time.Duration,
) (*stage.ToolPlanLease, error) {
	lease, err := safeLeaseGeneration(ctx, source, id)
	lease, err = validateAcquiredLease(lease, err, id.String(), releaseTimeout)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, releaseLeaseOnRollback(lease, err, releaseTimeout)
	}
	if lease.ToolPlanID() != id {
		return nil, releaseLeaseOnRollback(
			lease,
			fmt.Errorf("tool plan source returned generation %q for requested %q", lease.ToolPlanID(), id),
			releaseTimeout,
		)
	}
	return lease, nil
}

func safeLeaseCurrent(
	ctx context.Context,
	source stage.ToolPlanSource,
) (lease *stage.ToolPlanLease, err error) {
	defer func() {
		if recover() != nil {
			lease = nil
			err = errors.New("tool plan source LeaseCurrent panicked")
		}
	}()
	return source.LeaseCurrent(ctx)
}

func safeLeaseGeneration(
	ctx context.Context,
	source stage.ToolPlanSource,
	id stage.PlanID,
) (lease *stage.ToolPlanLease, err error) {
	defer func() {
		if recover() != nil {
			lease = nil
			err = errors.New("tool plan source LeaseGeneration panicked")
		}
	}()
	return source.LeaseGeneration(ctx, id)
}

func validateAcquiredLease(
	lease *stage.ToolPlanLease,
	acquireErr error,
	description string,
	releaseTimeout time.Duration,
) (*stage.ToolPlanLease, error) {
	if acquireErr != nil {
		if lease != nil {
			return nil, releaseLeaseOnRollback(
				lease,
				fmt.Errorf("tool plan source returned generation and error for %s acquisition", description),
				releaseTimeout,
			)
		}
		return nil, fmt.Errorf("lease tool plan %s: %w", description, acquireErr)
	}
	if lease == nil {
		return nil, fmt.Errorf("tool plan source returned nil for %s acquisition", description)
	}
	if err := lease.Validate(); err != nil {
		return nil, releaseLeaseOnRollback(
			lease, fmt.Errorf("validate tool plan %s: %w", description, err), releaseTimeout,
		)
	}
	return lease, nil
}

func releaseLeaseOnRollback(lease *stage.ToolPlanLease, cause error, timeout time.Duration) error {
	releaseContext, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if releaseErr := lease.ReleaseContext(releaseContext); releaseErr != nil {
		return errors.Join(cause, fmt.Errorf("release rolled-back tool plan: %w", releaseErr))
	}
	return cause
}

// ResumeSnapshot is the compatibility wrapper that prepares and immediately
// commits an exclusively owned suspended snapshot using ctx as the run root.
// Callers must ensure the original process/run authority is no longer executing.
func (engine *Engine) ResumeSnapshot(ctx context.Context, snapshot Snapshot) (*Run, error) {
	prepared, err := engine.PrepareResumeSnapshot(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	return prepared.Commit(ctx)
}

// Close rejects new runs and waits for cooperative active runs to drain.
func (engine *Engine) Close(ctx context.Context) error {
	return engine.stop(ctx, false)
}

// Shutdown rejects new runs, requests cancellation, and waits up to ctx.
func (engine *Engine) Shutdown(ctx context.Context) error {
	return engine.stop(ctx, true)
}

func (engine *Engine) stop(ctx context.Context, cancelActive bool) error {
	if ctx == nil {
		return errors.New("agent engine stop context must not be nil")
	}
	if engine == nil {
		return errors.New("agent engine is nil")
	}
	engine.mu.Lock()
	engine.closed = true
	drained := engine.drained
	runs := make([]*Run, 0, len(engine.active))
	for _, run := range engine.active {
		runs = append(runs, run)
	}
	engine.mu.Unlock()
	if cancelActive {
		for _, run := range runs {
			run.Cancel()
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-drained:
		return nil
	}
}

func (engine *Engine) executeState(ctx context.Context, run *Run, definition Definition, history []message.Message, firstTurn uint32, emitRunStart bool) {
	emitter := run.emitter
	var runErr error
	terminalKind := event.RunCompleted
	defer func() {
		if recovered := recover(); recovered != nil {
			runErr = errors.Join(runErr, fmt.Errorf("agent execution panic: %v", recovered))
			terminalKind = event.RunFailed
		}
		run.beginFinalization()
		releaseContext, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), engine.finalizationTimeout)
		releaseErr := run.planLease.ReleaseContext(releaseContext)
		releaseCancel()
		if releaseErr != nil {
			runErr = errors.Join(
				runErr,
				fmt.Errorf("release tool plan %q: %w", run.ToolPlanID(), releaseErr),
			)
			terminalKind = event.RunFailed
		}
		terminalErr := emitter.terminal(ctx, terminalKind, runErr)
		terminalCommitted := terminalErr == nil || committed(terminalErr)
		if terminalCommitted {
			run.recordTerminal(terminalKind, history)
		}
		run.complete(errors.Join(runErr, terminalErr))
	}()
	if emitRunStart {
		if err := emitter.lifecycleStart(ctx, map[string]string{"definition": definition.name}); err != nil {
			run.markStarted(committed(err))
			runErr = err
			terminalKind = event.RunFailed
			return
		}
		run.markStarted(true)
	}
	for turn := firstTurn; turn <= definition.maxTurns; turn++ {
		completed, err := engine.executeTurn(ctx, emitter, definition, turn, &history)
		if err != nil {
			runErr = err
			terminalKind = event.RunFailed
			if ctx.Err() != nil {
				terminalKind = event.RunCancelled
			}
			return
		}
		run.recordBoundary(turn, history)
		if completed {
			return
		}
		if err = run.suspendAtBoundary(ctx); err != nil {
			runErr = err
			terminalKind = event.RunFailed
			if ctx.Err() != nil {
				terminalKind = event.RunCancelled
			}
			return
		}
	}
	runErr = fmt.Errorf("agent run exceeded maximum turns %d", definition.maxTurns)
	terminalKind = event.RunFailed
}

func (engine *Engine) executeTurn(ctx context.Context, emitter *runEmitter, definition Definition, turn uint32, history *[]message.Message) (bool, error) {
	if err := emitter.emit(ctx, event.TurnStarted, map[string]uint32{"turn": turn}); err != nil {
		if committed(err) {
			return false, errors.Join(err, emitter.turnFailure(ctx, err))
		}
		return false, err
	}
	operationID := emitter.run.id + "/model/" + strconv.FormatUint(uint64(turn), 10)
	request, err := model.NewRequest(model.OperationID(operationID), definition.model, *history, emitter.run.dispatcher.Definitions())
	if err != nil {
		return false, errors.Join(err, emitter.turnFailure(ctx, err))
	}
	if err = emitter.emit(ctx, event.ModelStarted, map[string]any{"turn": turn, "operation_id": operationID}); err != nil {
		if committed(err) {
			return false, errors.Join(err,
				emitter.modelFailure(ctx, err),
				emitter.turnFailure(ctx, err))
		}
		return false, errors.Join(err, emitter.turnFailure(ctx, err))
	}
	stream, err := safeStream(ctx, engine.provider, request)
	if err != nil {
		normalized := normalizeStartError(err)
		return false, errors.Join(normalized,
			emitter.modelFailure(ctx, normalized),
			emitter.turnFailure(ctx, normalized))
	}
	text, calls, usage, metadata, err := consumeStream(ctx, emitter, stream)
	if err != nil {
		return false, errors.Join(err,
			emitter.modelFailure(ctx, err),
			emitter.turnFailure(ctx, err))
	}
	completedPayload := modelCompletedPayload{
		InputTokens: usage.InputTokens(), OutputTokens: usage.OutputTokens(),
		Metadata: engine.filterMetadata(metadata),
	}
	if err = emitter.emit(ctx, event.ModelCompleted, completedPayload); err != nil {
		return false, errors.Join(err, emitter.turnFailure(ctx, err))
	}
	if len(calls) == 0 {
		if err = emitter.emit(ctx, event.TurnCompleted, map[string]uint32{"turn": turn}); err != nil {
			return false, err
		}
		return true, nil
	}
	if err = engine.appendToolRound(ctx, emitter, text, calls, history); err != nil {
		return false, errors.Join(err, emitter.turnFailure(ctx, err))
	}
	if err = emitter.emit(ctx, event.TurnCompleted, map[string]uint32{"turn": turn}); err != nil {
		return false, err
	}
	return false, nil
}

func consumeStream(ctx context.Context, emitter *runEmitter, stream model.Stream) (textResult string, callsResult []tool.Call, usageResult model.Usage, metadataResult []model.Metadata, returnErr error) {
	if stream == nil {
		return "", nil, model.Usage{}, nil, errors.New("model provider returned a nil stream")
	}
	defer func() { returnErr = errors.Join(returnErr, safeClose(stream)) }()
	var text strings.Builder
	var calls []tool.Call
	observed := false
	for {
		streamEvent, err := safeRecv(ctx, stream)
		if err != nil {
			return "", nil, model.Usage{}, nil, normalizeRecvError(err, observed)
		}
		if err = streamEvent.Validate(); err != nil {
			return "", nil, model.Usage{}, nil, fmt.Errorf("validate model stream: %w", err)
		}
		observed = true
		switch streamEvent.Kind() {
		case model.EventTextDelta:
			value, _ := streamEvent.Text()
			if text.Len()+len(value) > model.MaximumOperationTextBytes {
				return "", nil, model.Usage{}, nil, fmt.Errorf("model stream text exceeds %d bytes", model.MaximumOperationTextBytes)
			}
			text.WriteString(value)
			if err = emitter.emit(ctx, event.ModelDelta, map[string]string{"text": value}); err != nil {
				return "", nil, model.Usage{}, nil, err
			}
		case model.EventToolCall:
			call, _ := streamEvent.Call()
			if len(calls) >= model.MaximumOperationToolCalls {
				return "", nil, model.Usage{}, nil, fmt.Errorf("model stream tool calls exceed %d", model.MaximumOperationToolCalls)
			}
			calls = append(calls, call)
		case model.EventCompleted:
			usage, _ := streamEvent.Usage()
			metadata, _ := streamEvent.Metadata()
			return text.String(), calls, usage, metadata, nil
		case model.EventFailed:
			problem, _ := streamEvent.Problem()
			operationErr, constructionErr := model.NewOperationError(problem, true, nil)
			return "", nil, model.Usage{}, nil, errors.Join(operationErr, constructionErr)
		}
	}
}

func normalizeRecvError(err error, observed bool) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if streamError, ok := errors.AsType[*model.StreamError](err); ok {
		operationError, constructionErr := model.NewOperationError(streamError.Problem(), observed, err)
		return errors.Join(operationError, constructionErr)
	}
	if errors.Is(err, io.EOF) {
		err = model.RequireCompletion(err, false)
	}
	problem, constructionErr := model.NewProblem("provider_stream", "model provider stream failed", false)
	if constructionErr != nil {
		return errors.Join(err, constructionErr)
	}
	operationError, operationConstructionErr := model.NewOperationError(problem, observed, err)
	return errors.Join(operationError, operationConstructionErr)
}

type modelMetadataPayload struct {
	Namespace string          `json:"namespace"`
	Value     json.RawMessage `json:"value"`
}

type modelCompletedPayload struct {
	InputTokens  uint64                 `json:"input_tokens"`
	OutputTokens uint64                 `json:"output_tokens"`
	Metadata     []modelMetadataPayload `json:"metadata,omitempty"`
}

type modelFailedPayload struct {
	Code         string                 `json:"code"`
	Message      string                 `json:"message"`
	Retryable    bool                   `json:"retryable"`
	BeforeStream bool                   `json:"before_stream"`
	Metadata     []modelMetadataPayload `json:"metadata,omitempty"`
}

type toolTerminalPayload struct {
	CallID  string                `json:"call_id"`
	Name    string                `json:"name"`
	Error   string                `json:"error"`
	Outcome tool.ExecutionState   `json:"outcome,omitempty"`
	Retry   tool.RetryDisposition `json:"retry,omitempty"`
}

func (engine *Engine) filterMetadata(metadata []model.Metadata) []modelMetadataPayload {
	byNamespace := make(map[string]modelMetadataPayload, len(metadata))
	for _, value := range metadata {
		if _, allowed := engine.metadataNamespaces[value.Namespace()]; !allowed {
			continue
		}
		byNamespace[value.Namespace()] = modelMetadataPayload{Namespace: value.Namespace(), Value: value.Value()}
	}
	result := make([]modelMetadataPayload, 0, len(byNamespace))
	for _, namespace := range slices.Sorted(maps.Keys(byNamespace)) {
		result = append(result, byNamespace[namespace])
	}
	return result
}

func normalizeStartError(err error) error {
	if providerError, ok := errors.AsType[*model.ProviderError](err); ok {
		operationErr, constructionErr := model.NewOperationError(providerError.Problem(), false, err)
		return errors.Join(operationErr, constructionErr)
	}
	problem, problemErr := model.NewProblem("provider_start", "model provider could not start the operation", false)
	if problemErr != nil {
		return errors.Join(err, problemErr)
	}
	operationErr, constructionErr := model.NewOperationError(problem, false, err)
	return errors.Join(operationErr, constructionErr)
}

func (engine *Engine) appendToolRound(ctx context.Context, emitter *runEmitter, textValue string, calls []tool.Call, history *[]message.Message) error {
	if err := validateNewToolCalls(*history, calls); err != nil {
		return err
	}
	parts := make([]message.Part, 0, len(calls)+1)
	if textValue != "" {
		part, err := message.Text(textValue)
		if err != nil {
			return err
		}
		parts = append(parts, part)
	}
	for _, call := range calls {
		part, err := message.ToolCall(string(call.ID()), call.Name(), call.Arguments())
		if err != nil {
			return err
		}
		parts = append(parts, part)
	}
	assistantMessage, err := engine.newMessage(emitter.run, message.RoleAssistant, parts...)
	if err != nil {
		return err
	}
	*history = append(*history, assistantMessage)
	for _, call := range calls {
		if err = emitter.emit(ctx, event.ToolStarted, map[string]string{"call_id": string(call.ID()), "name": call.Name()}); err != nil {
			if committed(err) {
				return errors.Join(err, emitter.toolFailure(ctx, call, err))
			}
			return err
		}
		result, dispatchErr := safeDispatch(ctx, emitter.run.dispatcher, call, emitter)
		if dispatchErr != nil {
			return errors.Join(dispatchErr, emitter.toolFailure(ctx, call, dispatchErr))
		}
		terminalKind := event.ToolCompleted
		problem, failed := result.Problem()
		if failed {
			terminalKind = event.ToolFailed
		}
		payload := toolTerminalPayload{CallID: string(call.ID()), Name: call.Name(), Error: problem}
		if err = emitter.toolTerminal(ctx, terminalKind, payload); err != nil {
			return err
		}
		part, partErr := message.ToolResult(string(call.ID()), call.Name(), result.Content())
		if partErr != nil {
			return partErr
		}
		resultMessage, messageErr := engine.newMessage(emitter.run, message.RoleTool, part)
		if messageErr != nil {
			return messageErr
		}
		*history = append(*history, resultMessage)
	}
	return nil
}

func validateNewToolCalls(history []message.Message, calls []tool.Call) error {
	seen := make(map[tool.CallID]struct{})
	for _, value := range history {
		for _, part := range value.Parts() {
			if part.Kind() == message.PartToolCall {
				seen[tool.CallID(part.CallID())] = struct{}{}
			}
		}
	}
	for _, call := range calls {
		if _, duplicate := seen[call.ID()]; duplicate {
			return fmt.Errorf("model tool call ID %q is duplicated in run history", call.ID())
		}
		seen[call.ID()] = struct{}{}
	}
	return nil
}

func (engine *Engine) newMessage(run *Run, role message.Role, parts ...message.Part) (message.Message, error) {
	id := run.id + "/message/" + strconv.FormatUint(run.emitter.NextSequence(), 10)
	messageID, err := message.NewID(id)
	if err != nil {
		return message.Message{}, err
	}
	result, err := message.New(messageID, role, parts...)
	if err != nil {
		return message.Message{}, err
	}
	run.stateMu.Lock()
	defer run.stateMu.Unlock()
	if _, duplicate := run.messageIDs[messageID]; duplicate {
		return message.Message{}, fmt.Errorf("generated message ID %q collides with existing history", messageID)
	}
	run.messageIDs[messageID] = struct{}{}
	return result, nil
}

func safeStream(ctx context.Context, provider model.Provider, request model.Request) (stream model.Stream, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("model provider panic: %v", recovered)
		}
	}()
	return provider.Stream(ctx, request)
}

func safeRecv(ctx context.Context, stream model.Stream) (streamEvent model.StreamEvent, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("model stream panic: %v", recovered)
		}
	}()
	return stream.Recv(ctx)
}

func safeClose(stream model.Stream) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("model stream close panic: %v", recovered)
		}
	}()
	return stream.Close()
}

func safeDispatch(ctx context.Context, dispatcher stage.ToolDispatcher, call tool.Call, reporter tool.Reporter) (result tool.Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("tool %q panic: %v", call.Name(), recovered)
		}
	}()
	return dispatcher.Dispatch(ctx, call, reporter)
}

func safeInteraction(ctx context.Context, broker interaction.Broker, scope interaction.Scope, request interaction.Request) (response interaction.Response, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("interaction broker panic: %v", recovered)
		}
	}()
	return broker.Request(ctx, scope, request)
}

func (run *Run) markStarted(started bool) {
	run.stateMu.Lock()
	run.started = started
	if started {
		run.status = runStatusRunning
	}
	run.stateMu.Unlock()
}

func (run *Run) recordSequence(sequence uint64) {
	run.stateMu.Lock()
	run.lastSequence = sequence
	run.stateMu.Unlock()
}

func (run *Run) recordBoundary(turn uint32, history []message.Message) {
	run.stateMu.Lock()
	run.completedTurns = turn
	run.history = cloneHistory(history)
	run.stateMu.Unlock()
}

func (run *Run) suspendAtBoundary(ctx context.Context) error {
	run.stateMu.Lock()
	if !run.suspendRequested {
		run.stateMu.Unlock()
		return nil
	}
	waiter := run.suspendWaiter
	run.suspendRequested = false
	run.suspendWaiter = nil
	if len(run.activeInteractions) != 0 {
		failure := &UnsafeSnapshotError{Status: runStatusSuspending, ActiveInteractions: len(run.activeInteractions)}
		run.status = runStatusRunning
		run.stateMu.Unlock()
		waiter <- failure
		return nil
	}
	run.status = LifecycleSuspended
	run.resumeSignal = make(chan struct{})
	resumeSignal := run.resumeSignal
	run.stateMu.Unlock()
	waiter <- nil
	select {
	case <-ctx.Done():
		run.stateMu.Lock()
		prepared := run.localResume
		if prepared == nil {
			run.status = runStatusFinishing
			run.stateMu.Unlock()
			return ctx.Err()
		}
		decision := prepared.decision
		run.stateMu.Unlock()
		<-decision
		return ctx.Err()
	case <-resumeSignal:
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
}

func (run *Run) beginFinalization() {
	run.stateMu.Lock()
	run.status = runStatusFinishing
	if run.suspendRequested && run.suspendWaiter != nil {
		run.suspendWaiter <- &UnsafeSnapshotError{Status: runStatusFinishing, ActiveInteractions: len(run.activeInteractions)}
		run.suspendRequested = false
		run.suspendWaiter = nil
	}
	run.stateMu.Unlock()
	run.cancel()
	run.interactionWG.Wait()
}

func (run *Run) recordTerminal(kind event.Kind, history []message.Message) {
	status := LifecycleFailed
	switch kind {
	case event.RunCompleted:
		status = LifecycleCompleted
	case event.RunCancelled:
		status = LifecycleCancelled
	}
	run.stateMu.Lock()
	run.status = status
	run.history = cloneHistory(history)
	run.stateMu.Unlock()
}

func (run *Run) complete(err error) {
	run.finalize.Do(func() {
		run.mu.Lock()
		run.err = err
		run.mu.Unlock()
		run.log.Close()
		close(run.done)
		run.cancel()
		run.engine.release(run.id)
	})
}

func (engine *Engine) release(runID string) {
	engine.mu.Lock()
	delete(engine.active, runID)
	if len(engine.active) == 0 {
		close(engine.drained)
	}
	engine.mu.Unlock()
}

type runEmitter struct {
	engine *Engine
	run    *Run
	mu     sync.Mutex
	next   uint64
}

func (emitter *runEmitter) emit(ctx context.Context, kind event.Kind, payload any) error {
	if ctx == nil {
		return errors.New("event emission context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return emitter.persist(ctx, kind, payload)
}

func (emitter *runEmitter) lifecycleStart(ctx context.Context, payload any) error {
	startContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitter.engine.finalizationTimeout)
	defer cancel()
	return emitter.persist(startContext, event.RunStarted, payload)
}

func (emitter *runEmitter) terminal(ctx context.Context, kind event.Kind, runErr error) error {
	var payload any
	if runErr != nil {
		payload = map[string]string{"error": runErr.Error()}
	}
	finalizationContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitter.engine.finalizationTimeout)
	defer cancel()
	if err := emitter.persist(finalizationContext, kind, payload); err != nil {
		return &DurabilityError{Kind: kind, Cause: err}
	}
	return nil
}

func (emitter *runEmitter) turnFailure(ctx context.Context, err error) error {
	finalizationContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitter.engine.finalizationTimeout)
	defer cancel()
	if persistErr := emitter.persist(finalizationContext, event.TurnFailed, map[string]string{"error": err.Error()}); persistErr != nil {
		return &DurabilityError{Kind: event.TurnFailed, Cause: persistErr}
	}
	return nil
}

func (emitter *runEmitter) toolFailure(ctx context.Context, call tool.Call, err error) error {
	payload := toolTerminalPayload{
		CallID: string(call.ID()),
		Name:   call.Name(),
		Error:  boundedToolFailureMessage(err),
	}
	if failure, typed := errors.AsType[*tool.ExecutionError](err); typed && failure != nil &&
		failure.Validate() == nil && failure.CallID() == call.ID() {
		payload.Outcome = failure.State()
		payload.Retry = failure.RetryDisposition()
	}
	finalizationContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitter.engine.finalizationTimeout)
	defer cancel()
	if persistErr := emitter.persist(finalizationContext, event.ToolFailed, payload); persistErr != nil {
		return &DurabilityError{Kind: event.ToolFailed, Cause: persistErr}
	}
	return nil
}

func (emitter *runEmitter) toolTerminal(
	ctx context.Context,
	kind event.Kind,
	payload toolTerminalPayload,
) error {
	finalizationContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitter.engine.finalizationTimeout)
	defer cancel()
	if persistErr := emitter.persist(finalizationContext, kind, payload); persistErr != nil {
		return &DurabilityError{Kind: kind, Cause: persistErr}
	}
	return nil
}

func boundedToolFailureMessage(err error) string {
	message := "tool execution failed"
	if err != nil {
		candidate := strings.TrimSpace(strings.ToValidUTF8(err.Error(), "\uFFFD"))
		if candidate != "" {
			message = candidate
		}
	}
	if len(message) <= tool.MaximumExecutionErrorBytes {
		return message
	}
	const suffix = "..."
	cutoff := tool.MaximumExecutionErrorBytes - len(suffix)
	for cutoff > 0 && !utf8.ValidString(message[:cutoff]) {
		cutoff--
	}
	return message[:cutoff] + suffix
}

func (emitter *runEmitter) modelFailure(ctx context.Context, err error) error {
	payload := modelFailedPayload{Code: "model_operation", Message: "model operation failed"}
	if operationError, ok := errors.AsType[*model.OperationError](err); ok {
		problem := operationError.Problem()
		payload.Code = problem.Code()
		payload.Message = problem.Message()
		payload.Retryable = operationError.Retryable()
		payload.BeforeStream = operationError.BeforeStream()
		payload.Metadata = emitter.engine.filterMetadata(problem.Metadata())
	} else if ctx.Err() != nil {
		payload.Code = "cancelled"
		payload.Message = "model operation was cancelled"
	}
	finalizationContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitter.engine.finalizationTimeout)
	defer cancel()
	if persistErr := emitter.persist(finalizationContext, event.ModelFailed, payload); persistErr != nil {
		return &DurabilityError{Kind: event.ModelFailed, Cause: persistErr}
	}
	return nil
}

func (emitter *runEmitter) persist(ctx context.Context, kind event.Kind, payload any) error {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	var data json.RawMessage
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		data = encoded
	}
	envelope, err := event.Reconstruct(emitter.run.id, emitter.next, emitter.engine.clock(), kind, data)
	if err != nil {
		return err
	}
	if appendErr := emitter.run.log.Append(envelope); appendErr != nil {
		return fmt.Errorf("persist event %s: %w", kind, appendErr)
	}
	emitter.next++
	emitter.run.recordSequence(envelope.Sequence())
	for index, observer := range emitter.engine.observers {
		if publishErr := observer.Publish(ctx, envelope); publishErr != nil {
			return &EmissionError{
				Kind: kind, Sequence: envelope.Sequence(), Committed: true,
				Cause: fmt.Errorf("required observer %d: %w", index, publishErr),
			}
		}
	}
	for _, observer := range emitter.engine.bestEffort {
		observer.TryPublish(envelope)
	}
	return nil
}

// NextSequence returns the next uncommitted run sequence for deterministic IDs.
func (emitter *runEmitter) NextSequence() uint64 {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	return emitter.next
}

func (emitter *runEmitter) interactionFailure(ctx context.Context, kind event.Kind, id interaction.ID) error {
	finalizationContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitter.engine.finalizationTimeout)
	defer cancel()
	if err := emitter.persist(finalizationContext, kind, map[string]string{"id": string(id), "error": "interaction did not complete"}); err != nil {
		return &DurabilityError{Kind: kind, Cause: err}
	}
	return nil
}

// Report validates bounded progress and persists it before live publication.
func (emitter *runEmitter) Report(ctx context.Context, progress tool.Progress) error {
	if err := progress.Validate(); err != nil {
		return err
	}
	return emitter.emit(ctx, event.ToolProgress, map[string]string{"call_id": string(progress.CallID()), "message": progress.Message()})
}
