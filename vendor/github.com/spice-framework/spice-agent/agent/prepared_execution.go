package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/spice-framework/spice-agent/event"
	"github.com/spice-framework/spice-agent/interaction"
	"github.com/spice-framework/spice-agent/message"
	"github.com/spice-framework/spice-agent/stage"
)

var (
	// ErrPreparedExecutionCommitted rejects a second terminal decision after Commit.
	ErrPreparedExecutionCommitted = errors.New("agent prepared execution is committed")
	// ErrPreparedExecutionAborted rejects Commit after Abort or failed Commit.
	ErrPreparedExecutionAborted = errors.New("agent prepared execution is aborted")
	// ErrPreparedRunActivated rejects aborting or reactivating a committed run
	// after its execution gate has been released.
	ErrPreparedRunActivated = errors.New("agent prepared run is activated")
	// ErrPreparedRunAborted rejects activation after the committed run was
	// cancelled at its inert execution gate.
	ErrPreparedRunAborted = errors.New("agent prepared run is aborted")
)

type preparedExecutionState uint8

const (
	preparedExecutionReady preparedExecutionState = iota
	preparedExecutionCommitted
	preparedExecutionAborted
)

type preparedExecutionKind uint8

const (
	preparedStartKind preparedExecutionKind = iota
	preparedResumeKind
)

// PreparedStart exclusively owns a validated new-run log and current tool-plan
// lease. RunID is stable before Commit so an outer host can acquire authority
// before the run becomes visible or executable. An uncommitted value must close.
type PreparedStart struct{ execution *preparedExecution }

// PreparedResume exclusively owns a validated snapshot-tail log and exact
// tool-plan lease. An uncommitted value must close. The preparation context
// never becomes the resumed run lifetime.
type PreparedResume struct{ execution *preparedExecution }

// PreparedRun is a registered run held at an inert execution gate. Registration
// owns the run ID, event log, and tool-plan lease, but no provider, tool, stage,
// or interaction can execute until Activate succeeds. The caller must finish a
// prepared run with Activate, Abort, or Close.
type PreparedRun struct {
	mu       sync.Mutex
	state    preparedRunState
	run      *Run
	decision chan struct{}
	execute  preparedRunExecution
}

type preparedRunState uint8

const (
	preparedRunReady preparedRunState = iota
	preparedRunActivated
	preparedRunAborted
)

type preparedRunExecution struct {
	engine       *Engine
	definition   Definition
	history      []message.Message
	firstTurn    uint32
	emitRunStart bool
}

type preparedExecution struct {
	mu    sync.Mutex
	state preparedExecutionState
	kind  preparedExecutionKind

	engine         *Engine
	lease          *stage.ToolPlanLease
	log            *event.Log
	runID          string
	identityToken  uint64
	definition     Definition
	history        []message.Message
	completedTurns uint32
	lastSequence   uint64
	planIdentity   PlanIdentity
	status         LifecycleStatus
	started        bool
	firstTurn      uint32
	emitRunStart   bool

	seenInteractions map[interaction.ID]struct{}
	messageIDs       map[message.ID]struct{}
	cleanupErr       error
}

// PrepareStart validates and acquires every resource needed for a new run
// without registering it, publishing an event, or starting a goroutine.
// setupCtx owns setup and acquisition only; it never owns the run lifetime.
func (engine *Engine) PrepareStart(setupCtx context.Context, definition Definition, input Input) (*PreparedStart, error) {
	if setupCtx == nil {
		return nil, errors.New("agent start preparation context must not be nil")
	}
	if engine == nil {
		return nil, errors.New("agent engine is nil")
	}
	if err := setupCtx.Err(); err != nil {
		return nil, err
	}
	if _, err := NewDefinition(definition.name, definition.model, definition.maxTurns); err != nil {
		return nil, err
	}
	if _, err := NewInput(input.message); err != nil {
		return nil, err
	}
	if engine.isClosed() {
		return nil, errors.New("agent engine is closed")
	}
	lease, err := leaseCurrentToolPlan(setupCtx, engine.toolPlans, engine.finalizationTimeout)
	if err != nil {
		return nil, err
	}
	planIdentity, err := newPlanIdentity(engine.compiledPlan, engine.snapshotCompatibility, lease)
	if err != nil {
		return nil, releaseLeaseOnRollback(lease, err, engine.finalizationTimeout)
	}
	runID, err := engine.ids.Next("run")
	if err != nil {
		return nil, releaseLeaseOnRollback(lease, fmt.Errorf("allocate run ID: %w", err), engine.finalizationTimeout)
	}
	if err = snapshotToken("run ID", runID, 96); err != nil {
		return nil, releaseLeaseOnRollback(lease, err, engine.finalizationTimeout)
	}
	identityToken, err := engine.reserveRunIdentity(runID, preparedStartKind)
	if err != nil {
		return nil, releaseLeaseOnRollback(lease, err, engine.finalizationTimeout)
	}
	log, err := event.NewLog(runID, engine.logLimits)
	if err != nil {
		return nil, rollbackPreparedResources(engine, runID, identityToken, nil, lease, err)
	}
	if err = setupCtx.Err(); err != nil {
		return nil, rollbackPreparedResources(engine, runID, identityToken, log, lease, err)
	}
	return &PreparedStart{execution: &preparedExecution{
		state: preparedExecutionReady, kind: preparedStartKind,
		engine: engine, lease: lease, log: log, runID: runID, identityToken: identityToken,
		definition: definition, history: []message.Message{input.message.Clone()},
		planIdentity: planIdentity, firstTurn: 1, emitRunStart: true,
		seenInteractions: make(map[interaction.ID]struct{}),
		messageIDs:       map[message.ID]struct{}{input.message.ID(): {}},
	}}, nil
}

// PrepareResumeSnapshot validates and acquires every resource needed to resume
// snapshot without registering a run, publishing an event, or starting a
// goroutine. setupCtx owns setup and acquisition only; it never owns the run.
func (engine *Engine) PrepareResumeSnapshot(setupCtx context.Context, snapshot Snapshot) (*PreparedResume, error) {
	if setupCtx == nil {
		return nil, errors.New("agent resume preparation context must not be nil")
	}
	if engine == nil {
		return nil, errors.New("agent engine is nil")
	}
	if err := setupCtx.Err(); err != nil {
		return nil, err
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	if snapshot.status != LifecycleSuspended {
		return nil, &UnsafeSnapshotError{Status: snapshot.status}
	}
	if engine.snapshotCompatibility == "" {
		return nil, errors.New("agent engine snapshot import requires an explicit generated compatibility identity")
	}
	if snapshot.planIdentity.SnapshotCompatibilityIdentity() != engine.snapshotCompatibility ||
		!slices.Equal(snapshot.planIdentity.CompiledIdentities(), engine.compiledPlan) {
		return nil, errors.New("agent snapshot compiled compatibility does not match the constructed engine")
	}
	identityToken, err := engine.reserveRunIdentity(snapshot.runID, preparedResumeKind)
	if err != nil {
		return nil, err
	}
	lease, err := leaseToolPlanGeneration(
		setupCtx, engine.toolPlans, snapshot.planIdentity.ToolPlanID(), engine.finalizationTimeout,
	)
	if err != nil {
		return nil, errors.Join(err, engine.abortRunIdentityReservation(snapshot.runID, identityToken))
	}
	planIdentity, err := newPlanIdentity(engine.compiledPlan, engine.snapshotCompatibility, lease)
	if err != nil {
		return nil, rollbackPreparedResources(engine, snapshot.runID, identityToken, nil, lease, err)
	}
	if !snapshot.planIdentity.equal(planIdentity) {
		return nil, rollbackPreparedResources(
			engine, snapshot.runID, identityToken, nil, lease,
			errors.New("agent snapshot plan identity does not match the leased engine plan"),
		)
	}
	log, err := event.NewLogAfter(snapshot.runID, snapshot.lastSequence, engine.logLimits)
	if err != nil {
		return nil, rollbackPreparedResources(engine, snapshot.runID, identityToken, nil, lease, err)
	}
	if err = setupCtx.Err(); err != nil {
		return nil, rollbackPreparedResources(engine, snapshot.runID, identityToken, log, lease, err)
	}
	return &PreparedResume{execution: &preparedExecution{
		state: preparedExecutionReady, kind: preparedResumeKind,
		engine: engine, lease: lease, log: log, runID: snapshot.runID, identityToken: identityToken,
		definition: snapshot.definition, history: cloneHistory(snapshot.history),
		completedTurns: snapshot.completedTurns, lastSequence: snapshot.lastSequence,
		planIdentity: planIdentity, status: runStatusRunning, started: true,
		firstTurn:        snapshot.completedTurns + 1,
		seenInteractions: interactionIDSet(snapshot.interactionIDs),
		messageIDs:       snapshotMessageIDs(snapshot.history),
	}}, nil
}

func rollbackPreparedResources(engine *Engine, runID string, token uint64, log *event.Log, lease *stage.ToolPlanLease, cause error) error {
	if log != nil {
		log.Close()
	}
	releaseErr := releaseLeaseOnRollback(lease, cause, engine.finalizationTimeout)
	identityErr := engine.abortRunIdentityReservation(runID, token)
	return errors.Join(releaseErr, identityErr)
}

func preparedDuplicateError(kind preparedExecutionKind, runID string) error {
	if kind == preparedResumeKind {
		return fmt.Errorf("agent snapshot run ID %q was already imported", runID)
	}
	return fmt.Errorf("agent ID source returned duplicate run ID %q", runID)
}

// RunID returns the stable identity allocated during preparation.
func (prepared *PreparedStart) RunID() string {
	if prepared == nil || prepared.execution == nil {
		return ""
	}
	return prepared.execution.runID
}

// RunID returns the stable snapshot run identity.
func (prepared *PreparedResume) RunID() string {
	if prepared == nil || prepared.execution == nil {
		return ""
	}
	return prepared.execution.runID
}

// Commit registers the prepared new run and starts it exactly once using the
// caller-owned runRootCtx as its lifetime root.
func (prepared *PreparedStart) Commit(runRootCtx context.Context) (*Run, error) {
	if prepared == nil || prepared.execution == nil {
		return nil, errors.New("agent prepared start is nil")
	}
	committed, err := prepared.execution.commitPaused(runRootCtx, true)
	if err != nil {
		return nil, err
	}
	return committed.run, nil
}

// Commit registers the prepared resumed run and starts it exactly once using
// the caller-owned runRootCtx as its lifetime root.
func (prepared *PreparedResume) Commit(runRootCtx context.Context) (*Run, error) {
	if prepared == nil || prepared.execution == nil {
		return nil, errors.New("agent prepared resume is nil")
	}
	committed, err := prepared.execution.commitPaused(runRootCtx, true)
	if err != nil {
		return nil, err
	}
	return committed.run, nil
}

// CommitPaused registers the prepared new run under the caller-owned root but
// holds all execution behind an explicit activation gate.
func (prepared *PreparedStart) CommitPaused(runRootCtx context.Context) (*PreparedRun, error) {
	if prepared == nil || prepared.execution == nil {
		return nil, errors.New("agent prepared start is nil")
	}
	return prepared.execution.commitPaused(runRootCtx, false)
}

// CommitPaused registers the prepared snapshot run under the caller-owned root
// but holds all execution behind an explicit activation gate.
func (prepared *PreparedResume) CommitPaused(runRootCtx context.Context) (*PreparedRun, error) {
	if prepared == nil || prepared.execution == nil {
		return nil, errors.New("agent prepared resume is nil")
	}
	return prepared.execution.commitPaused(runRootCtx, false)
}

func (prepared *preparedExecution) commitPaused(runRootCtx context.Context, activateImmediately bool) (*PreparedRun, error) {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	switch prepared.state {
	case preparedExecutionCommitted:
		return nil, ErrPreparedExecutionCommitted
	case preparedExecutionAborted:
		return nil, errors.Join(ErrPreparedExecutionAborted, prepared.cleanupErr)
	}
	if runRootCtx == nil {
		return nil, prepared.abortLocked(errors.New("agent run root context must not be nil"))
	}
	if err := runRootCtx.Err(); err != nil {
		return nil, prepared.abortLocked(err)
	}
	runContext, cancel := context.WithCancel(runRootCtx)
	run := &Run{
		id: prepared.runID, log: prepared.log, done: make(chan struct{}), cancel: cancel,
		engine: prepared.engine, ctx: runContext, definition: prepared.definition,
		history: cloneHistory(prepared.history), completedTurns: prepared.completedTurns,
		status: prepared.status, started: prepared.started, lastSequence: prepared.lastSequence,
		dispatcher: prepared.lease.Dispatcher(), planLease: prepared.lease,
		planIdentity:       prepared.planIdentity.clone(),
		activeInteractions: make(map[interaction.ID]struct{}),
		seenInteractions:   maps.Clone(prepared.seenInteractions),
		messageIDs:         maps.Clone(prepared.messageIDs),
		identityToken:      prepared.identityToken,
	}
	run.identityRetirement = &RunIdentityRetirement{
		engine: prepared.engine, runID: prepared.runID, token: prepared.identityToken,
	}
	run.emitter = &runEmitter{engine: prepared.engine, run: run, next: prepared.lastSequence + 1}

	prepared.engine.mu.Lock()
	if prepared.engine.closed {
		prepared.engine.mu.Unlock()
		cancel()
		return nil, prepared.abortLocked(errors.New("agent engine is closed"))
	}
	if activateImmediately {
		if err := prepared.engine.identities.transition(prepared.runID, prepared.identityToken, runIdentityReserved, runIdentityActive); err != nil {
			prepared.engine.mu.Unlock()
			cancel()
			return nil, prepared.abortLocked(err)
		}
	}
	if len(prepared.engine.active) == 0 {
		prepared.engine.drained = make(chan struct{})
	}
	prepared.engine.active[prepared.runID] = run
	prepared.state = preparedExecutionCommitted
	prepared.log = nil
	prepared.lease = nil
	prepared.engine.mu.Unlock()

	committed := &PreparedRun{
		state: preparedRunReady, run: run, decision: make(chan struct{}),
		execute: preparedRunExecution{
			engine: prepared.engine, definition: prepared.definition, history: cloneHistory(prepared.history),
			firstTurn: prepared.firstTurn, emitRunStart: prepared.emitRunStart,
		},
	}
	if activateImmediately {
		committed.state = preparedRunActivated
		close(committed.decision)
	}
	go committed.awaitDecision()
	return committed, nil
}

// RunID returns the stable registered run identity.
func (prepared *PreparedRun) RunID() string {
	if prepared == nil || prepared.run == nil {
		return ""
	}
	return prepared.run.ID()
}

// Activate releases the gate exactly once and returns the registered run.
func (prepared *PreparedRun) Activate() (*Run, error) {
	if prepared == nil || prepared.run == nil {
		return nil, errors.New("agent prepared run is nil")
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	switch prepared.state {
	case preparedRunActivated:
		return nil, ErrPreparedRunActivated
	case preparedRunAborted:
		return nil, ErrPreparedRunAborted
	default:
		if err := prepared.execute.engine.activateRunIdentity(prepared.run.ID(), prepared.run.identityToken); err != nil {
			return nil, err
		}
		prepared.state = preparedRunActivated
		close(prepared.decision)
		return prepared.run, nil
	}
}

// Abort cancels an inert committed run and joins its bounded terminal
// finalization. It never releases the execution gate into provider or tool work.
func (prepared *PreparedRun) Abort(ctx context.Context) error {
	if prepared == nil || prepared.run == nil {
		return errors.New("agent prepared run is nil")
	}
	if ctx == nil {
		return errors.New("agent prepared run abort context is nil")
	}
	prepared.mu.Lock()
	switch prepared.state {
	case preparedRunActivated:
		prepared.mu.Unlock()
		return ErrPreparedRunActivated
	case preparedRunAborted:
		prepared.mu.Unlock()
		return prepared.wait(ctx)
	default:
		prepared.state = preparedRunAborted
		prepared.run.Cancel()
		close(prepared.decision)
		prepared.mu.Unlock()
		return prepared.wait(ctx)
	}
}

// Close aborts an inert committed run using the engine's bounded finalization
// timeout. It is idempotent after a completed abort.
func (prepared *PreparedRun) Close() error {
	if prepared == nil || prepared.run == nil {
		return errors.New("agent prepared run is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*prepared.execute.engine.finalizationTimeout)
	defer cancel()
	return prepared.Abort(ctx)
}

func (prepared *PreparedRun) awaitDecision() {
	<-prepared.decision
	prepared.mu.Lock()
	state := prepared.state
	prepared.mu.Unlock()
	if state == preparedRunAborted {
		prepared.finalizeAbort()
		return
	}
	prepared.execute.engine.executeState(
		prepared.run.ctx,
		prepared.run,
		prepared.execute.definition,
		cloneHistory(prepared.execute.history),
		prepared.execute.firstTurn,
		prepared.execute.emitRunStart,
	)
}

func (prepared *PreparedRun) finalizeAbort() {
	prepared.run.beginFinalization()
	releaseContext, cancel := context.WithTimeout(context.Background(), prepared.execute.engine.finalizationTimeout)
	err := prepared.run.planLease.ReleaseContext(releaseContext)
	cancel()
	if err != nil {
		err = fmt.Errorf("release tool plan %q from inert run: %w", prepared.run.ToolPlanID(), err)
	}
	prepared.run.completeInert(err)
}

func (prepared *PreparedRun) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-prepared.run.done:
		prepared.run.mu.Lock()
		defer prepared.run.mu.Unlock()
		return prepared.run.err
	}
}

// Abort closes and releases an uncommitted new-run preparation exactly once.
func (prepared *PreparedStart) Abort() error {
	if prepared == nil || prepared.execution == nil {
		return errors.New("agent prepared start is nil")
	}
	return prepared.execution.abort()
}

// Close implements the standard cleanup form for an uncommitted start.
func (prepared *PreparedStart) Close() error { return prepared.Abort() }

// Abort closes and releases an uncommitted resume preparation exactly once.
func (prepared *PreparedResume) Abort() error {
	if prepared == nil || prepared.execution == nil {
		return errors.New("agent prepared resume is nil")
	}
	return prepared.execution.abort()
}

// Close implements the standard cleanup form for an uncommitted resume.
func (prepared *PreparedResume) Close() error { return prepared.Abort() }

func (prepared *preparedExecution) abort() error {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	switch prepared.state {
	case preparedExecutionCommitted:
		return ErrPreparedExecutionCommitted
	case preparedExecutionAborted:
		return prepared.cleanupErr
	default:
		return prepared.abortLocked(nil)
	}
}

func (prepared *preparedExecution) abortLocked(cause error) error {
	prepared.state = preparedExecutionAborted
	if prepared.log != nil {
		prepared.log.Close()
		prepared.log = nil
	}
	if prepared.lease != nil {
		releaseContext, cancel := context.WithTimeout(context.Background(), prepared.engine.finalizationTimeout)
		prepared.cleanupErr = prepared.lease.ReleaseContext(releaseContext)
		cancel()
		if prepared.cleanupErr != nil {
			prepared.cleanupErr = fmt.Errorf("release aborted tool plan: %w", prepared.cleanupErr)
		}
		prepared.lease = nil
	}
	identityErr := prepared.engine.abortRunIdentityReservation(prepared.runID, prepared.identityToken)
	prepared.cleanupErr = errors.Join(prepared.cleanupErr, identityErr)
	return errors.Join(cause, prepared.cleanupErr)
}
