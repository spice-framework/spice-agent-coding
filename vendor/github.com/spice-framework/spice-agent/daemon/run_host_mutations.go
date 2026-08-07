package daemon

import (
	"context"
	"crypto/sha256"
	"errors"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/client"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
	"github.com/spice-framework/spice-agent/interaction"
	"github.com/spice-framework/spice-agent/message"
	"google.golang.org/protobuf/proto"
)

const (
	mutationStart   = "run.start/v1"
	mutationImport  = "run.import/v1"
	mutationSuspend = "run.suspend/v1"
	mutationResume  = "run.resume/v1"
	mutationCancel  = "run.cancel/v1"
	mutationRespond = "run.respond/v1"
)

// Start creates a generated definition run without allowing any kernel work
// before the corresponding authority record is durably ACTIVE.
func (host *RunHost) Start(ctx context.Context, session Session, request client.StartRequest) (client.StartResult, error) {
	if host == nil {
		return client.StartResult{}, ErrRunHostClosed
	}
	if err := request.Validate(); err != nil {
		return client.StartResult{}, ErrRunHostState
	}
	if err := host.beginOperation(); err != nil {
		return client.StartResult{}, err
	}
	defer host.endOperation()
	digest := canonicalMutationDigest(struct {
		DefinitionID       string `json:"definition_id"`
		DefinitionRevision string `json:"definition_revision"`
		MessageID          string `json:"message_id"`
		Text               string `json:"text"`
	}{request.Definition().ID(), request.Definition().Revision(), request.Input().MessageID(), request.Input().Text()})
	outcome, duplicate, err := host.doMutation(ctx, session, request.Operation(), mutationStart, digest, func(operationContext context.Context) Outcome {
		return host.start(operationContext, session, request)
	})
	if err != nil {
		return client.StartResult{}, err
	}
	run, err := client.NewRunRef(outcome.RunID)
	if err != nil {
		return client.StartResult{}, ErrRunHostUnavailable
	}
	result, err := client.NewStartResult(run, outcome.Sequence, outcome.PlanID, duplicate)
	if err != nil {
		return client.StartResult{}, ErrRunHostUnavailable
	}
	return result, nil
}

func (host *RunHost) start(ctx context.Context, session Session, request client.StartRequest) (result Outcome) {
	reservation, err := host.reserveSlot(session.ClientID())
	if err != nil {
		return abandonRunHostOutcome(err)
	}
	defer reservation.release()
	prepared, preparationFailure := host.prepareStart(ctx, request)
	if preparationFailure.Kind() != "" {
		return preparationFailure
	}
	preparedOwned := true
	defer func() {
		if preparedOwned {
			if abortErr := prepared.Abort(); abortErr != nil {
				host.degrade(degradedLifecycleCleanup)
				result = failureRunHostOutcome(ErrRunHostUncertain)
			}
		}
	}()

	runID := prepared.RunID()
	binding, err := reservation.bind(runID)
	if err != nil {
		return bindingFailureOutcome(err)
	}
	releaseSetup := true
	defer func() {
		if releaseSetup {
			binding.Release()
		}
	}()
	committed, err := prepared.CommitPaused(host.root)
	if err != nil {
		preparedOwned = false
		return host.failedPausedCommit(prepared, err)
	}
	preparedOwned = false
	outcome, published := host.activateAndPublishStart(ctx, session, reservation, committed, binding)
	if published {
		releaseSetup = false
	}
	return outcome
}

func (host *RunHost) prepareStart(ctx context.Context, request client.StartRequest) (*agent.PreparedStart, Outcome) {
	definition, err := host.definitions.Resolve(request.Definition().ID(), request.Definition().Revision())
	if err != nil {
		return nil, failureRunHostOutcome(ErrRunHostState)
	}
	input, err := makeAgentInput(request.Input())
	if err != nil {
		return nil, failureRunHostOutcome(ErrRunHostState)
	}
	prepared, err := host.engine.PrepareStart(ctx, definition.Agent(), input)
	if err != nil {
		return nil, host.preparationFailure(err)
	}
	return prepared, Outcome{}
}

func (host *RunHost) preparationFailure(err error) Outcome {
	if errors.Is(err, agent.ErrRunIdentityCapacity) {
		if isIsolatedRunIdentityCapacity(err) {
			return abandonRunHostOutcome(publicRunHostError(err))
		}
		host.degrade(degradedLifecycleCleanup)
		return failureRunHostOutcome(ErrRunHostUncertain)
	}
	if isIsolatedContextTermination(err) {
		return abandonRunHostOutcome(err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		host.degrade(degradedLifecycleCleanup)
		return failureRunHostOutcome(ErrRunHostUncertain)
	}
	return failureRunHostOutcome(publicRunHostError(err))
}

func isIsolatedRunIdentityCapacity(err error) bool {
	for err != nil {
		if _, wrapsMany := err.(interface{ Unwrap() []error }); wrapsMany {
			return false
		}
		wrapped, wraps := err.(interface{ Unwrap() error })
		if !wraps {
			return errors.Is(err, agent.ErrRunIdentityCapacity)
		}
		err = wrapped.Unwrap()
	}
	return false
}

func bindingFailureOutcome(err error) Outcome {
	if errors.Is(err, ErrRunHostClosed) || errors.Is(err, ErrRunHostCapacity) {
		return abandonRunHostOutcome(err)
	}
	return failureRunHostOutcome(err)
}

func (host *RunHost) failedPausedCommit(prepared *agent.PreparedStart, err error) Outcome {
	if cleanupErr := prepared.Abort(); cleanupErr != nil {
		host.degrade(degradedLifecycleCleanup)
		return failureRunHostOutcome(ErrRunHostUncertain)
	}
	if isIsolatedContextTermination(err) {
		return abandonRunHostOutcome(publicRunHostError(err))
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		host.degrade(degradedLifecycleCleanup)
		return failureRunHostOutcome(ErrRunHostUncertain)
	}
	if errors.Is(err, ErrRunHostClosed) {
		return abandonRunHostOutcome(publicRunHostError(err))
	}
	return failureRunHostOutcome(publicRunHostError(err))
}

func (host *RunHost) precommitOperationFailure(err error) Outcome {
	if !isIsolatedContextTermination(err) &&
		(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		host.degrade(degradedLifecycleCleanup)
		return failureRunHostOutcome(ErrRunHostUncertain)
	}
	return abandonRunHostOutcome(publicRunHostError(err))
}

func (host *RunHost) activateAndPublishStart(
	ctx context.Context,
	session Session,
	reservation *hostReservation,
	committed *agent.PreparedRun,
	binding *RunBinding,
) (Outcome, bool) {
	candidate := host.trackPaused(committed)

	lease, err := host.sessions.AcquireMutationCommit(ctx, session.ClientID(), session.Epoch())
	if err != nil {
		if abortErr := host.abortPaused(candidate); abortErr != nil {
			return failureRunHostOutcome(ErrRunHostUncertain), false
		}
		return host.precommitOperationFailure(err), false
	}
	defer lease.Close()

	candidate.mu.Lock()
	if candidate.decided || host.root.Err() != nil {
		candidate.mu.Unlock()
		if abortErr := host.abortPaused(candidate); abortErr != nil {
			return failureRunHostOutcome(ErrRunHostUncertain), false
		}
		return abandonRunHostOutcome(ErrRunHostClosed), false
	}
	transitionContext, cancel := host.transitionContext()
	authority, authorityErr := host.authority.Start(transitionContext, committed.RunID())
	cancel()
	if authorityErr != nil {
		candidate.mu.Unlock()
		cleanupErr := host.abortPaused(candidate)
		if cleanupErr != nil {
			return failureRunHostOutcome(ErrRunHostUncertain), false
		}
		return host.precommitAuthorityFailure(authorityErr), false
	}
	run, activationErr := committed.Activate()
	candidate.decided = true
	candidate.mu.Unlock()
	host.untrackPaused(candidate)
	if activationErr != nil {
		host.abortPreparedAndThen(committed, func() error {
			host.rollbackStartedAuthority(authority)
			return nil
		})
		return failureRunHostOutcome(ErrRunHostUncertain), false
	}
	host.publish(reservation, run, authority, binding)
	return successRunHostOutcome(runHostOutcome{
		RunID: committed.RunID(), Sequence: 1, PlanID: run.ToolPlanID().String(),
	}), true
}

func makeAgentInput(value client.Input) (agent.Input, error) {
	id, err := message.NewID(value.MessageID())
	if err != nil {
		return agent.Input{}, err
	}
	part, err := message.Text(value.Text())
	if err != nil {
		return agent.Input{}, err
	}
	initial, err := message.New(id, message.RoleUser, part)
	if err != nil {
		return agent.Input{}, err
	}
	return agent.NewInput(initial)
}

// Import authenticates and consumes a suspended authority snapshot before
// either authority or kernel execution becomes visible.
func (host *RunHost) Import(ctx context.Context, session Session, request client.ImportRequest) (client.ImportResult, error) {
	if host == nil {
		return client.ImportResult{}, ErrRunHostClosed
	}
	encoded, envelope, snapshot, err := decodeImport(request)
	if err != nil {
		return client.ImportResult{}, ErrRunHostState
	}
	if err = host.beginOperation(); err != nil {
		return client.ImportResult{}, err
	}
	defer host.endOperation()
	digest := canonicalMutationDigest(struct {
		Snapshot [32]byte `json:"snapshot"`
	}{sha256.Sum256(encoded)})
	outcome, duplicate, err := host.doMutation(ctx, session, request.Operation(), mutationImport, digest, func(operationContext context.Context) Outcome {
		return host.importRun(operationContext, session, envelope, snapshot)
	})
	if err != nil {
		return client.ImportResult{}, err
	}
	run, err := client.NewRunRef(outcome.RunID)
	if err != nil {
		return client.ImportResult{}, ErrRunHostUnavailable
	}
	result, err := client.NewImportResult(run, outcome.Sequence, duplicate)
	if err != nil {
		return client.ImportResult{}, ErrRunHostUnavailable
	}
	return result, nil
}

func decodeImport(request client.ImportRequest) ([]byte, *enginev1.SnapshotEnvelope, agent.Snapshot, error) {
	if err := request.Operation().Validate(); err != nil {
		return nil, nil, agent.Snapshot{}, err
	}
	encoded, err := request.Snapshot().MarshalBinary()
	if err != nil {
		return nil, nil, agent.Snapshot{}, err
	}
	envelope := new(enginev1.SnapshotEnvelope)
	if err = proto.Unmarshal(encoded, envelope); err != nil || len(envelope.ProtoReflect().GetUnknown()) != 0 {
		return nil, nil, agent.Snapshot{}, errors.New("snapshot envelope is invalid")
	}
	if err = enginev1.ValidateSnapshotEnvelope(envelope); err != nil {
		return nil, nil, agent.Snapshot{}, err
	}
	snapshot, err := agent.ParseSnapshot(envelope.GetPayload())
	if err != nil || snapshot.RunID() != envelope.GetRunId() || snapshot.LastSequence() != envelope.GetLastSequence() ||
		snapshot.Status() != agent.LifecycleSuspended {
		return nil, nil, agent.Snapshot{}, errors.New("snapshot payload does not match envelope")
	}
	return encoded, envelope, snapshot, nil
}

func (host *RunHost) importRun(
	ctx context.Context,
	session Session,
	envelope *enginev1.SnapshotEnvelope,
	snapshot agent.Snapshot,
) Outcome {
	runID := snapshot.RunID()
	reservation, err := host.reserveSlot(session.ClientID())
	if err != nil {
		return abandonRunHostOutcome(err)
	}
	defer reservation.release()
	binding, err := reservation.bind(runID)
	if err != nil {
		return bindingFailureOutcome(err)
	}
	releaseSetup := true
	defer func() {
		if releaseSetup {
			binding.Release()
		}
	}()
	transaction, prepared, preparationFailure := host.prepareImport(ctx, envelope, snapshot)
	if preparationFailure.Kind() != "" {
		return preparationFailure
	}
	outcome, published := host.activateAndPublishImport(
		ctx, session, reservation, transaction, prepared, binding, snapshot.LastSequence()+1,
	)
	if published {
		releaseSetup = false
	}
	return outcome
}

func (host *RunHost) prepareImport(
	ctx context.Context,
	envelope *enginev1.SnapshotEnvelope,
	snapshot agent.Snapshot,
) (hostImportAuthority, *agent.PreparedResume, Outcome) {
	transaction, err := host.authority.PrepareImport(ctx, cloneEnvelope(envelope))
	if err != nil {
		if transaction != nil {
			if abortErr := transaction.Abort(); abortErr != nil {
				host.classifyAuthorityFailure(abortErr, degradedLifecycleCleanup)
				return nil, nil, failureRunHostOutcome(ErrRunHostUncertain)
			}
		}
		return nil, nil, host.precommitAuthorityFailure(err)
	}
	if transaction == nil {
		return nil, nil, host.precommitAuthorityFailure(ErrRunAuthorityUnavailable)
	}
	prepared, err := host.engine.PrepareResumeSnapshot(ctx, snapshot)
	if err == nil {
		return transaction, prepared, Outcome{}
	}
	outcome := host.preparationFailure(err)
	if abortErr := transaction.Abort(); abortErr != nil {
		host.classifyAuthorityFailure(abortErr, degradedLifecycleCleanup)
		outcome = failureRunHostOutcome(ErrRunHostUncertain)
	}
	return nil, nil, outcome
}

func (host *RunHost) precommitAuthorityFailure(err error) Outcome {
	host.classifyAuthorityFailure(err, "")
	if isIsolatedContextTermination(err) {
		return abandonRunHostOutcome(err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		host.degrade(degradedAuthorityUncertain)
		return failureRunHostOutcome(ErrRunHostUncertain)
	}
	if errors.Is(err, ErrRunAuthorityBusy) || errors.Is(err, ErrRunAuthorityUnavailable) {
		return abandonRunHostOutcome(publicRunHostError(err))
	}
	return failureRunHostOutcome(publicRunHostError(err))
}

func (host *RunHost) activateAndPublishImport(
	ctx context.Context,
	session Session,
	reservation *hostReservation,
	transaction hostImportAuthority,
	prepared *agent.PreparedResume,
	binding *RunBinding,
	nextSequence uint64,
) (result Outcome, published bool) {
	if transaction == nil || prepared == nil {
		cleanupFailed := false
		if prepared != nil && prepared.Abort() != nil {
			host.degrade(degradedLifecycleCleanup)
			cleanupFailed = true
		}
		if transaction != nil && transaction.Abort() != nil {
			host.degrade(degradedAuthorityUncertain)
			cleanupFailed = true
		}
		if cleanupFailed {
			return failureRunHostOutcome(ErrRunHostUncertain), false
		}
		host.degrade(degradedAuthorityMissing)
		return failureRunHostOutcome(ErrRunHostUnavailable), false
	}
	transactionOwned := true
	defer func() {
		if transactionOwned {
			if abortErr := transaction.Abort(); abortErr != nil {
				host.classifyAuthorityFailure(abortErr, degradedLifecycleCleanup)
				result = failureRunHostOutcome(ErrRunHostUncertain)
			}
		}
	}()
	preparedOwned := true
	defer func() {
		if preparedOwned {
			if abortErr := prepared.Abort(); abortErr != nil {
				host.degrade(degradedLifecycleCleanup)
				result = failureRunHostOutcome(ErrRunHostUncertain)
			}
		}
	}()

	lease, err := host.sessions.AcquireMutationCommit(ctx, session.ClientID(), session.Epoch())
	if err != nil {
		return host.precommitOperationFailure(err), false
	}
	defer lease.Close()
	if outcome, consumed := host.consumeImport(transaction); !consumed {
		return outcome, false
	}

	committed, commitErr := prepared.CommitPaused(host.root)
	if commitErr != nil {
		cleanupErr := prepared.Abort()
		preparedOwned = false
		if cleanupErr != nil {
			host.degrade(degradedLifecycleCleanup)
		}
		transactionOwned = false
		if abortErr := transaction.Abort(); abortErr != nil {
			host.degrade(degradedAuthorityUncertain)
		}
		host.degrade(degradedAuthorityUncertain)
		return failureRunHostOutcome(ErrRunHostUncertain), false
	}
	preparedOwned = false
	transactionOwned = false
	return host.activateCommittedImport(reservation, transaction, committed, binding, nextSequence)
}

func (host *RunHost) consumeImport(transaction hostImportAuthority) (Outcome, bool) {
	if transaction == nil {
		host.degrade(degradedAuthorityMissing)
		return failureRunHostOutcome(ErrRunHostUnavailable), false
	}
	transitionContext, cancel := host.transitionContext()
	consumeErr := transaction.Consume(transitionContext)
	cancel()
	if consumeErr == nil {
		return Outcome{}, true
	}
	return host.precommitAuthorityFailure(consumeErr), false
}

func (host *RunHost) activateCommittedImport(
	reservation *hostReservation,
	transaction hostImportAuthority,
	committed *agent.PreparedRun,
	binding *RunBinding,
	nextSequence uint64,
) (Outcome, bool) {
	if transaction == nil {
		host.abortPreparedAndThen(committed, nil)
		host.degrade(degradedAuthorityMissing)
		return failureRunHostOutcome(ErrRunHostUnavailable), false
	}
	candidate := host.trackPaused(committed)
	candidate.mu.Lock()
	if candidate.decided || host.root.Err() != nil {
		candidate.decided = true
		candidate.mu.Unlock()
		host.untrackPaused(candidate)
		host.abortPreparedAndThen(committed, transaction.Abort)
		return failureRunHostOutcome(ErrRunHostUncertain), false
	}
	transitionContext, cancel := host.transitionContext()
	authority, activationErr := transaction.Activate(transitionContext)
	cancel()
	if activationErr != nil {
		candidate.decided = true
		candidate.mu.Unlock()
		host.untrackPaused(candidate)
		host.abortPreparedAndThen(committed, transaction.Abort)
		host.classifyAuthorityFailure(activationErr, degradedAuthorityUncertain)
		return failureRunHostOutcome(ErrRunHostUncertain), false
	}
	run, kernelErr := committed.Activate()
	candidate.decided = true
	candidate.mu.Unlock()
	host.untrackPaused(candidate)
	if kernelErr != nil {
		host.abortPreparedAndThen(committed, func() error {
			host.rollbackStartedAuthority(authority)
			return nil
		})
		return failureRunHostOutcome(ErrRunHostUncertain), false
	}
	host.publish(reservation, run, authority, binding)
	return successRunHostOutcome(runHostOutcome{RunID: committed.RunID(), Sequence: nextSequence}), true
}

// Suspend waits for a kernel safe boundary without holding the client mutation
// fence, then commits only the bounded authority issuance under that fence.
func (host *RunHost) Suspend(ctx context.Context, session Session, request client.RunMutation) (client.SuspendResult, error) {
	if host == nil {
		return client.SuspendResult{}, ErrRunHostClosed
	}
	if err := validateRunMutation(request); err != nil {
		return client.SuspendResult{}, ErrRunHostState
	}
	if err := host.beginOperation(); err != nil {
		return client.SuspendResult{}, err
	}
	defer host.endOperation()
	digest := canonicalMutationDigest(struct {
		RunID string `json:"run_id"`
	}{request.Run().ID()})
	outcome, duplicate, err := host.doMutation(ctx, session, request.Operation(), mutationSuspend, digest, func(operationContext context.Context) Outcome {
		return host.suspend(operationContext, session, request.Run().ID())
	})
	if err != nil {
		return client.SuspendResult{}, err
	}
	result, err := client.NewSuspendResult(true, outcome.Sequence, duplicate)
	if err != nil {
		return client.SuspendResult{}, ErrRunHostUnavailable
	}
	return result, nil
}

func (host *RunHost) suspend(ctx context.Context, session Session, runID string) Outcome {
	value, err := host.ownedActive(session.ClientID(), runID)
	if err != nil {
		return failureRunHostOutcome(err)
	}
	waitContext, cancelWait := mergeContexts(ctx, session.Context())
	if err = value.transition.LockContext(waitContext); err != nil {
		cancelWait()
		return host.precommitOperationFailure(err)
	}
	defer value.transition.Unlock()
	defer cancelWait()
	err = value.run.Suspend(waitContext)
	if err != nil {
		if isIsolatedContextTermination(err) {
			return abandonRunHostOutcome(err)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			host.degrade(degradedLifecycleCleanup)
			return failureRunHostOutcome(ErrRunHostUncertain)
		}
		return failureRunHostOutcome(publicRunHostError(err))
	}
	lease, err := host.sessions.AcquireMutationCommit(ctx, session.ClientID(), session.Epoch())
	if err != nil {
		if restoreErr := host.restoreSuspendedRun(value); restoreErr != nil {
			return failureRunHostOutcome(restoreErr)
		}
		return host.precommitOperationFailure(err)
	}
	defer lease.Close()
	snapshot, err := value.run.ExportSnapshot()
	if err != nil {
		if restoreErr := host.restoreSuspendedRun(value); restoreErr != nil {
			return failureRunHostOutcome(restoreErr)
		}
		return failureRunHostOutcome(ErrRunHostState)
	}
	transitionContext, cancel := host.transitionContext()
	envelope, err := value.authority.IssueSnapshotEnvelope(transitionContext, snapshot)
	cancel()
	if err != nil {
		if errors.Is(err, ErrRunAuthorityUncertain) {
			value.run.Cancel()
			host.degrade(degradedAuthorityUncertain)
			return failureRunHostOutcome(ErrRunHostUncertain)
		}
		if restoreErr := host.restoreSuspendedRun(value); restoreErr != nil {
			return failureRunHostOutcome(restoreErr)
		}
		return host.precommitAuthorityFailure(err)
	}
	value.snapshot = cloneEnvelope(envelope)
	return successRunHostOutcome(runHostOutcome{RunID: runID, Sequence: snapshot.LastSequence()})
}

func (host *RunHost) restoreSuspendedRun(value *hostedRun) error {
	prepared, err := value.run.PrepareLocalResume()
	if err == nil {
		err = prepared.Commit()
		if err == nil {
			return nil
		}
		_ = prepared.Abort()
	}
	value.run.Cancel()
	joinContext, cancel := host.transitionContext()
	joinErr := value.run.Wait(joinContext)
	cancel()
	host.degrade(degradedLifecycleCleanup)
	if joinErr != nil && !errors.Is(joinErr, context.Canceled) {
		host.degrade(degradedLifecycleCleanup)
	}
	return ErrRunHostUncertain
}

// Resume invalidates the authority snapshot before releasing local execution.
func (host *RunHost) Resume(ctx context.Context, session Session, request client.RunMutation) (client.ResumeResult, error) {
	if host == nil {
		return client.ResumeResult{}, ErrRunHostClosed
	}
	if err := validateRunMutation(request); err != nil {
		return client.ResumeResult{}, ErrRunHostState
	}
	if err := host.beginOperation(); err != nil {
		return client.ResumeResult{}, err
	}
	defer host.endOperation()
	digest := canonicalMutationDigest(struct {
		RunID string `json:"run_id"`
	}{request.Run().ID()})
	outcome, duplicate, err := host.doMutation(ctx, session, request.Operation(), mutationResume, digest, func(operationContext context.Context) Outcome {
		return host.resume(operationContext, session, request.Run().ID())
	})
	if err != nil {
		return client.ResumeResult{}, err
	}
	result, err := client.NewResumeResult(true, outcome.Sequence, duplicate)
	if err != nil {
		return client.ResumeResult{}, ErrRunHostUnavailable
	}
	return result, nil
}

func (host *RunHost) resume(ctx context.Context, session Session, runID string) Outcome {
	value, err := host.ownedActive(session.ClientID(), runID)
	if err != nil {
		return failureRunHostOutcome(err)
	}
	waitContext, cancelWait := mergeContexts(ctx, session.Context())
	if err = value.transition.LockContext(waitContext); err != nil {
		cancelWait()
		return host.precommitOperationFailure(err)
	}
	defer value.transition.Unlock()
	defer cancelWait()
	prepared, err := value.run.PrepareLocalResume()
	if err != nil {
		return failureRunHostOutcome(ErrRunHostState)
	}
	lease, err := host.sessions.AcquireMutationCommit(ctx, session.ClientID(), session.Epoch())
	if err != nil {
		if abortErr := host.abortLocalResume(value, prepared); abortErr != nil {
			return failureRunHostOutcome(abortErr)
		}
		return host.precommitOperationFailure(err)
	}
	defer lease.Close()
	transitionContext, cancel := host.transitionContext()
	err = value.authority.Resume(transitionContext)
	cancel()
	if err != nil {
		if errors.Is(err, ErrRunAuthorityUncertain) {
			value.run.Cancel()
			host.degrade(degradedAuthorityUncertain)
			if abortErr := host.abortLocalResume(value, prepared); abortErr != nil {
				return failureRunHostOutcome(abortErr)
			}
			return failureRunHostOutcome(ErrRunHostUncertain)
		}
		if abortErr := host.abortLocalResume(value, prepared); abortErr != nil {
			return failureRunHostOutcome(abortErr)
		}
		return host.precommitAuthorityFailure(err)
	}
	if err = prepared.Commit(); err != nil {
		value.run.Cancel()
		host.degrade(degradedLifecycleCleanup)
		return failureRunHostOutcome(ErrRunHostUncertain)
	}
	value.snapshot = nil
	return successRunHostOutcome(runHostOutcome{RunID: runID, Sequence: prepared.NextSequence()})
}

func (host *RunHost) abortLocalResume(value *hostedRun, prepared *agent.PreparedLocalResume) error {
	if err := prepared.Abort(); err == nil {
		return nil
	}
	value.run.Cancel()
	joinContext, cancel := host.transitionContext()
	_ = value.run.Wait(joinContext)
	cancel()
	host.degrade(degradedLifecycleCleanup)
	return ErrRunHostUncertain
}

// Cancel records exactly one cooperative cancellation request per operation.
func (host *RunHost) Cancel(ctx context.Context, session Session, request client.CancelRequest) (client.CancelResult, error) {
	if host == nil {
		return client.CancelResult{}, ErrRunHostClosed
	}
	if err := validateCancelRequest(request); err != nil {
		return client.CancelResult{}, ErrRunHostState
	}
	if err := host.beginOperation(); err != nil {
		return client.CancelResult{}, err
	}
	defer host.endOperation()
	digest := canonicalMutationDigest(struct {
		RunID  string `json:"run_id"`
		Reason string `json:"reason"`
	}{request.Run().ID(), request.Reason()})
	outcome, _, err := host.doMutation(ctx, session, request.Operation(), mutationCancel, digest, func(operationContext context.Context) Outcome {
		active, terminal, lookupErr := host.ownedRun(session.ClientID(), request.Run().ID())
		if lookupErr != nil {
			return failureRunHostOutcome(lookupErr)
		}
		if terminal != nil {
			if terminal.sequence == 0 {
				return failureRunHostOutcome(ErrRunHostState)
			}
			return successRunHostOutcome(runHostOutcome{Sequence: terminal.sequence, Flag: true})
		}
		if active == nil {
			return failureRunHostOutcome(ErrRunHostUnavailable)
		}
		if transitionErr := active.transition.LockContext(operationContext); transitionErr != nil {
			return host.precommitOperationFailure(transitionErr)
		}
		defer active.transition.Unlock()
		active, terminal, lookupErr = host.ownedRun(session.ClientID(), request.Run().ID())
		if lookupErr != nil {
			return failureRunHostOutcome(lookupErr)
		}
		if terminal != nil {
			if terminal.sequence == 0 {
				return failureRunHostOutcome(ErrRunHostState)
			}
			return successRunHostOutcome(runHostOutcome{Sequence: terminal.sequence, Flag: true})
		}
		if active == nil {
			return failureRunHostOutcome(ErrRunHostUnavailable)
		}
		if snapshot, snapshotErr := active.run.ExportSnapshot(); snapshotErr == nil &&
			(snapshot.Status() == agent.LifecycleCompleted || snapshot.Status() == agent.LifecycleFailed ||
				snapshot.Status() == agent.LifecycleCancelled) {
			return successRunHostOutcome(runHostOutcome{Sequence: snapshot.LastSequence(), Flag: true})
		}
		lease, acquireErr := host.sessions.AcquireMutationCommit(operationContext, session.ClientID(), session.Epoch())
		if acquireErr != nil {
			return host.precommitOperationFailure(acquireErr)
		}
		active.run.Cancel()
		lease.Close()
		return successRunHostOutcome(runHostOutcome{Flag: false})
	})
	if err != nil {
		return client.CancelResult{}, err
	}
	result, err := client.NewCancelResult(!outcome.Flag, outcome.Flag, outcome.Sequence)
	if err != nil {
		return client.CancelResult{}, ErrRunHostUnavailable
	}
	return result, nil
}

// Respond completes an accepted interaction even after its run reaches a
// terminal boundary; the pending binding owns that drain lifetime.
func (host *RunHost) Respond(ctx context.Context, session Session, request client.RespondRequest) (client.RespondResult, error) {
	if host == nil {
		return client.RespondResult{}, ErrRunHostClosed
	}
	if err := validateRespondRequest(request); err != nil {
		return client.RespondResult{}, ErrRunHostState
	}
	encoded, err := request.Response().Value().EncodeTransfer()
	if err != nil {
		return client.RespondResult{}, ErrRunHostState
	}
	if err = host.beginOperation(); err != nil {
		return client.RespondResult{}, err
	}
	defer host.endOperation()
	digest := canonicalMutationDigest(struct {
		RunID       string   `json:"run_id"`
		Interaction string   `json:"interaction"`
		Value       [32]byte `json:"value"`
	}{request.Run().ID(), request.Response().ID(), sha256.Sum256(encoded)})
	outcome, duplicate, err := host.doMutation(ctx, session, request.Operation(), mutationRespond, digest, func(operationContext context.Context) Outcome {
		if !host.owns(session.ClientID(), request.Run().ID()) {
			return failureRunHostOutcome(ErrHostedRunUnavailable)
		}
		lease, acquireErr := host.sessions.AcquireMutationCommit(operationContext, session.ClientID(), session.Epoch())
		if acquireErr != nil {
			return host.precommitOperationFailure(acquireErr)
		}
		defer lease.Close()
		scope, scopeErr := interaction.NewScope(request.Run().ID())
		if scopeErr != nil {
			return failureRunHostOutcome(ErrRunHostState)
		}
		response, responseErr := interaction.NewResponse(interaction.ID(request.Response().ID()), encoded)
		if responseErr != nil {
			return failureRunHostOutcome(ErrRunHostState)
		}
		if responseErr = host.pending.Respond(session.ClientID(), scope, response); responseErr != nil {
			return failureRunHostOutcome(publicRunHostError(responseErr))
		}
		return successRunHostOutcome(runHostOutcome{Flag: true})
	})
	if err != nil {
		return client.RespondResult{}, err
	}
	result, err := client.NewRespondResult(outcome.Flag, duplicate)
	if err != nil {
		return client.RespondResult{}, ErrRunHostUnavailable
	}
	return result, nil
}

func validateRunMutation(request client.RunMutation) error {
	_, err := client.NewRunMutation(request.Run(), request.Operation())
	return err
}

func validateCancelRequest(request client.CancelRequest) error {
	_, err := client.NewCancelRequest(request.Run(), request.Operation(), request.Reason())
	return err
}

func validateRespondRequest(request client.RespondRequest) error {
	_, err := client.NewRespondRequest(request.Run(), request.Operation(), request.Response())
	return err
}

func mergeContexts(first, second context.Context) (context.Context, context.CancelFunc) {
	if first == nil {
		first = context.Background()
	}
	if second == nil {
		second = context.Background()
	}
	ctx, cancel := context.WithCancel(first)
	stop := context.AfterFunc(second, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
