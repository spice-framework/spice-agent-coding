package pluginhost

import (
	"context"
	"time"

	"github.com/spice-framework/spice-agent/stage"
)

type recoveryClock interface {
	wait(context.Context, time.Duration) error
}

type systemRecoveryClock struct{}

func (systemRecoveryClock) wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type recoveryEpisode struct {
	generation       *hostGeneration
	planID           stage.PlanID
	desired          Set
	desiredRevision  uint64
	explicitRevision uint64
	attempts         uint32
	failed           bool
	exhausted        bool
	cancel           context.CancelFunc
}

func (host *Host) recoveryController() {
	defer close(host.recoveryDone)
	for {
		select {
		case <-host.rootDone:
			return
		case <-host.recoveryWake:
		}
		for host.recoverOnce() {
		}
	}
}

func (host *Host) recoverOnce() bool {
	host.mu.Lock()
	episode := host.recovery
	if episode == nil || episode.exhausted || host.closing {
		host.mu.Unlock()
		return false
	}
	attempt := episode.attempts + 1
	operation, cancel := context.WithCancel(host.root)
	episode.cancel = cancel
	host.signalLocked()
	host.mu.Unlock()

	defer func() {
		cancel()
		host.mu.Lock()
		if host.recovery == episode {
			episode.cancel = nil
		}
		host.signalLocked()
		host.mu.Unlock()
	}()

	if backoff := host.restart.backoff(attempt); backoff > 0 {
		if err := host.clock.wait(operation, backoff); err != nil {
			return false
		}
	}
	attemptContext, attemptCancel := context.WithTimeout(operation, host.restart.attemptTimeout)
	next, err := host.stage(attemptContext, episode.desired)
	if err != nil {
		attemptCancel()
		return host.finishRecoveryFailure(episode)
	}
	if !host.publishRecovery(attemptContext, next, episode) {
		attemptCancel()
		host.cleanAborted(next.candidates)
		return host.finishRecoveryFailure(episode)
	}
	attemptCancel()
	return false
}

func (host *Host) finishRecoveryFailure(episode *recoveryEpisode) bool {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.recovery != episode || !host.recoveryMatchesLocked(episode) {
		return false
	}
	episode.attempts++
	episode.failed = true
	episode.exhausted = episode.attempts >= host.restart.maximumAttempts
	host.signalLocked()
	return !episode.exhausted
}

func (host *Host) publishRecovery(
	ctx context.Context,
	next *hostGeneration,
	episode *recoveryEpisode,
) bool {
	host.mu.Lock()
	if ctx.Err() != nil || host.recovery != episode || !host.recoveryMatchesLocked(episode) ||
		candidateHealth(next.candidates) != nil {
		host.mu.Unlock()
		return false
	}
	episode.attempts++
	previous := host.current
	next.current = true
	previous.current = false
	host.current = next
	host.available[next.id] = next
	host.owned[next] = struct{}{}
	host.recovery = nil
	if previous.refs == 0 {
		host.queueCleanupLocked(previous, 0)
	}
	host.signalLocked()
	host.mu.Unlock()
	for _, candidate := range next.candidates {
		go host.observe(next, candidate)
	}
	return true
}

func (host *Host) recoveryMatchesLocked(episode *recoveryEpisode) bool {
	return !host.closing && host.explicitActivations == 0 && host.current == episode.generation &&
		host.current.id == episode.planID &&
		host.desiredRevision == episode.desiredRevision && host.explicitRevision == episode.explicitRevision &&
		host.current.unhealthy != nil
}

func (host *Host) scheduleRecoveryLocked(generation *hostGeneration) {
	if host.closing || host.explicitActivations != 0 || !host.restart.Enabled() || !host.hasDesired ||
		host.current != generation || generation.unhealthy == nil {
		return
	}
	if host.recovery != nil && host.recoveryMatchesLocked(host.recovery) {
		return
	}
	host.recovery = &recoveryEpisode{
		generation: generation, planID: generation.id, desired: host.desired,
		desiredRevision: host.desiredRevision, explicitRevision: host.explicitRevision,
	}
	select {
	case host.recoveryWake <- struct{}{}:
	default:
	}
	host.signalLocked()
}

func (host *Host) cancelRecoveryLocked() {
	if host.recovery != nil && host.recovery.cancel != nil {
		host.recovery.cancel()
	}
	host.recovery = nil
	host.signalLocked()
}

func cloneSet(set Set) Set {
	return Set{executables: set.Executables()}
}
