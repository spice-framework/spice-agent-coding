package daemon

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/client"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
	"google.golang.org/protobuf/proto"
)

func (host *RunHost) abortPaused(candidate *hostPausedRun) error {
	if candidate == nil {
		return nil
	}
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	if candidate.decided {
		return nil
	}
	candidate.decided = true
	ctx, cancel := host.transitionContext()
	defer cancel()
	err := candidate.prepared.Abort(ctx)
	host.untrackPaused(candidate)
	if err != nil {
		host.degrade(degradedLifecycleCleanup)
		return ErrRunHostUnavailable
	}
	return nil
}

func (host *RunHost) abortPreparedAndThen(prepared *agent.PreparedRun, release func() error) {
	if prepared == nil {
		if release != nil {
			_ = release()
		}
		return
	}
	ctx, cancel := host.transitionContext()
	err := prepared.Abort(ctx)
	cancel()
	if err == nil {
		if release != nil && release() != nil {
			host.degrade(degradedAuthorityUncertain)
		}
		return
	}
	host.degrade(degradedLifecycleCleanup)
	host.monitors.Go(func() {
		_ = prepared.Abort(context.Background())
		if release != nil && release() != nil {
			host.degrade(degradedAuthorityUncertain)
		}
	})
}

func (host *RunHost) rollbackStartedAuthority(authority hostActiveAuthority) {
	if authority == nil {
		return
	}
	ctx, cancel := host.transitionContext()
	err := authority.Terminal(ctx, TerminalCancelled)
	cancel()
	if errors.Is(err, ErrRunAuthorityUncertain) {
		host.degrade(degradedAuthorityUncertain)
	} else if err != nil {
		host.degrade(degradedAuthorityMissing)
	}
	if closeErr := authority.Close(); closeErr != nil {
		host.degrade(degradedLifecycleCleanup)
	}
}

func (host *RunHost) monitor(value *hostedRun) {
	defer host.monitors.Done()
	_ = value.run.Wait(context.Background())

	value.transition.Lock()
	snapshot, exportErr := value.run.ExportSnapshot()
	var envelope *enginev1.SnapshotEnvelope
	var issueErr error
	if exportErr == nil {
		ctx, cancel := host.transitionContext()
		envelope, issueErr = value.authority.IssueSnapshotEnvelope(ctx, snapshot)
		cancel()
	}
	if exportErr != nil {
		host.degrade(degradedTerminalSnapshot)
	} else if issueErr != nil {
		host.classifyAuthorityFailure(issueErr, degradedTerminalSnapshot)
	}
	if err := value.authority.Close(); err != nil {
		host.classifyAuthorityFailure(err, degradedLifecycleCleanup)
	}
	host.finishRun(value, snapshot, envelope, exportErr, issueErr)
	value.transition.Unlock()

	value.binding.Release()
	_ = value.binding.WaitReleased(context.Background())
	host.mu.Lock()
	if host.owners[value.run.ID()] == value.clientID {
		delete(host.owners, value.run.ID())
	}
	host.mu.Unlock()
}

func (host *RunHost) finishRun(
	value *hostedRun,
	snapshot agent.Snapshot,
	envelope *enginev1.SnapshotEnvelope,
	exportErr error,
	issueErr error,
) {
	entry := &terminalRun{clientID: value.clientID, run: value.run}
	if exportErr == nil {
		entry.sequence = snapshot.LastSequence()
	}
	if exportErr == nil && issueErr == nil {
		encoded, err := marshalEnvelope(envelope)
		if err != nil || len(encoded) > host.terminalMaxBytes {
			host.degrade(degradedTerminalSnapshot)
		} else {
			entry.envelope = cloneEnvelope(envelope)
			entry.bytes = len(encoded)
		}
	}

	host.mu.Lock()
	delete(host.active, value.run.ID())
	if host.activeReserved > 0 {
		host.activeReserved--
	}
	host.nextOrdinal++
	entry.ordinal = host.nextOrdinal
	host.terminal[value.run.ID()] = entry
	host.terminalOrder = append(host.terminalOrder, value.run.ID())
	host.terminalBytes += entry.bytes
	host.evictTerminalsLocked()
	host.mu.Unlock()
}

func (host *RunHost) evictTerminalsLocked() {
	for len(host.terminalOrder) > host.terminalMaxRuns || host.terminalBytes > host.terminalMaxBytes {
		runID := host.terminalOrder[0]
		host.terminalOrder = host.terminalOrder[1:]
		entry := host.terminal[runID]
		if entry == nil {
			continue
		}
		host.terminalBytes -= entry.bytes
		delete(host.terminal, runID)
	}
}

func (host *RunHost) classifyAuthorityFailure(err error, fallback string) {
	switch {
	case errors.Is(err, ErrRunAuthorityUncertain):
		host.degrade(degradedAuthorityUncertain)
	case errors.Is(err, ErrRunAuthorityUnavailable):
		host.degrade(degradedAuthorityMissing)
	case fallback != "":
		host.degrade(fallback)
	}
}

func cloneEnvelope(value *enginev1.SnapshotEnvelope) *enginev1.SnapshotEnvelope {
	if value == nil {
		return nil
	}
	return proto.CloneOf(value)
}

func marshalEnvelope(value *enginev1.SnapshotEnvelope) ([]byte, error) {
	if value == nil {
		return nil, errors.New("snapshot envelope is nil")
	}
	return (proto.MarshalOptions{Deterministic: true}).Marshal(value)
}

// Health reports only the immutable configured server limits. Callers cannot
// inject negotiated limits into readiness accounting.
func (host *RunHost) Health(ctx context.Context, session Session) (client.Health, error) {
	if host == nil || ctx == nil {
		return client.Health{}, ErrRunHostClosed
	}
	if err := ctx.Err(); err != nil {
		return client.Health{}, err
	}
	if err := host.sessions.Check(session.ClientID(), session.Epoch()); err != nil {
		return client.Health{}, publicRunHostError(err)
	}
	description, err := host.snapshotDescription()
	if err != nil {
		return client.Health{}, err
	}
	return description.Health(), nil
}

func (host *RunHost) healthAssumingLocked() (client.Health, error) {
	state := client.HealthReady
	reasons := slices.Sorted(maps.Keys(host.degraded))
	if host.closing {
		state = client.HealthStopping
		reasons = nil
	} else if len(reasons) != 0 {
		state = client.HealthDegraded
	}
	active := host.activeReserved
	limits := host.limits
	health, err := client.NewHealth(state, reasons, active, limits)
	if err != nil {
		return client.Health{}, err
	}
	return health, nil
}

// Export returns only a cached, authority-issued safe-boundary envelope.
func (host *RunHost) Export(ctx context.Context, session Session, run client.RunRef) (client.Snapshot, error) {
	if host == nil {
		return client.Snapshot{}, ErrRunHostClosed
	}
	if err := run.Validate(); err != nil {
		return client.Snapshot{}, ErrHostedRunUnavailable
	}
	if err := host.beginOperation(); err != nil {
		return client.Snapshot{}, err
	}
	defer host.endOperation()
	if ctx == nil {
		return client.Snapshot{}, context.Canceled
	}
	if err := host.sessions.Check(session.ClientID(), session.Epoch()); err != nil {
		return client.Snapshot{}, publicRunHostError(err)
	}
	active, terminal, err := host.ownedRun(session.ClientID(), run.ID())
	if err != nil {
		return client.Snapshot{}, err
	}
	var envelope *enginev1.SnapshotEnvelope
	if active != nil {
		waitContext, cancelWait := mergeContexts(ctx, session.Context())
		if err = active.transition.LockContext(waitContext); err != nil {
			cancelWait()
			return client.Snapshot{}, publicRunHostError(err)
		}
		cancelWait()
		if err = host.sessions.Check(session.ClientID(), session.Epoch()); err != nil {
			active.transition.Unlock()
			return client.Snapshot{}, publicRunHostError(err)
		}
		host.mu.Lock()
		stillActive := host.active[run.ID()] == active && active.clientID == session.ClientID()
		if stillActive {
			envelope = cloneEnvelope(active.snapshot)
		} else if completed := host.terminal[run.ID()]; completed != nil && completed.clientID == session.ClientID() {
			envelope = cloneEnvelope(completed.envelope)
		}
		host.mu.Unlock()
		active.transition.Unlock()
	} else {
		if terminal == nil {
			return client.Snapshot{}, ErrRunHostUnavailable
		}
		envelope = cloneEnvelope(terminal.envelope)
	}
	encoded, err := marshalEnvelope(envelope)
	if err != nil {
		return client.Snapshot{}, ErrRunHostState
	}
	result, err := client.ParseSnapshot(encoded)
	if err != nil {
		return client.Snapshot{}, ErrRunHostUnavailable
	}
	return result, nil
}

// Shutdown rejects admission, aborts inert candidates, cancels kernel work,
// drains accepted interactions, joins finalizers, and closes authority last.
// Cleanup continues even if one caller stops waiting.
func (host *RunHost) Shutdown(ctx context.Context) error {
	if host == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("run host shutdown context is nil")
	}
	host.shutdown.Do(func() {
		host.mu.Lock()
		host.closing = true
		host.mu.Unlock()
		host.cancelRoot(ErrRunHostClosed)
		go host.shutdownCore()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-host.shutdownDone:
		host.shutdownMu.Lock()
		defer host.shutdownMu.Unlock()
		return host.shutdownErr
	}
}

func (host *RunHost) shutdownCore() {
	host.mu.Lock()
	paused := slices.Collect(maps.Keys(host.paused))
	host.mu.Unlock()
	host.sessions.Close()
	host.pending.Close()
	for _, candidate := range paused {
		_ = host.abortPaused(candidate)
	}
	host.operations.Wait()

	host.mu.Lock()
	paused = slices.Collect(maps.Keys(host.paused))
	host.mu.Unlock()
	for _, candidate := range paused {
		_ = host.abortPaused(candidate)
	}

	var result error
	if err := host.engine.Shutdown(context.Background()); err != nil {
		host.degrade(degradedLifecycleCleanup)
		result = errors.Join(result, ErrRunHostUnavailable)
	}
	host.monitors.Wait()
	if err := host.sessions.Shutdown(context.Background()); err != nil {
		host.degrade(degradedLifecycleCleanup)
		result = errors.Join(result, ErrRunHostUnavailable)
	}
	if err := host.authority.Close(); err != nil {
		host.classifyAuthorityFailure(err, degradedLifecycleCleanup)
		result = errors.Join(result, publicRunHostError(err))
	}
	host.shutdownMu.Lock()
	host.shutdownErr = result
	host.shutdownMu.Unlock()
	close(host.shutdownDone)
}
