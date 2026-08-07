package daemon

import (
	"context"
	"errors"
	"sync"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/event"
)

func canceledObservationContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// EventObservation owns one session stream lease and one immutable replay
// page. A transport must call Close only after its sender has stopped using the
// page and tail. Close cancels and joins the internal tail before releasing the
// reconnect fence, and is safe to call repeatedly.
type EventObservation struct {
	page   event.ReplayPage
	ctx    context.Context //nolint:containedctx // the observation owns its stream lifetime.
	cancel context.CancelFunc
	lease  *StreamLease

	close sync.Once
}

// Page returns a defensive copy of the captured replay page. Tail, when
// present, belongs to this observation and remains valid until Close.
func (observation *EventObservation) Page() event.ReplayPage {
	if observation == nil {
		return event.ReplayPage{}
	}
	page := observation.page
	page.Events = append([]event.Envelope(nil), observation.page.Events...)
	return page
}

// Context is canceled by the caller, reconnect fencing, or daemon shutdown.
func (observation *EventObservation) Context() context.Context {
	if observation == nil || observation.ctx == nil {
		return canceledObservationContext()
	}
	return observation.ctx
}

// Close joins internal event delivery before releasing the session lease.
func (observation *EventObservation) Close() {
	if observation == nil {
		return
	}
	observation.close.Do(func() {
		if observation.cancel != nil {
			observation.cancel()
		}
		if observation.page.Tail != nil {
			_ = observation.page.Tail.Wait(context.Background())
		}
		observation.lease.Close()
	})
}

// InteractionObservation owns one session stream lease and either a complete
// finite snapshot or that snapshot plus a revision-contiguous tail. A
// transport must call Close only after its sender joins.
type InteractionObservation struct {
	snapshot     PendingSnapshot
	subscription *PendingSubscription
	ctx          context.Context //nolint:containedctx // the observation owns its stream lifetime.
	cancel       context.CancelFunc
	lease        *StreamLease

	close sync.Once
}

// Snapshot returns the complete defensive first frame.
func (observation *InteractionObservation) Snapshot() PendingSnapshot {
	if observation == nil {
		return PendingSnapshot{}
	}
	return clonePendingSnapshot(observation.snapshot)
}

// Deltas returns the live tail, or an already closed stream for a finite
// snapshot observation.
func (observation *InteractionObservation) Deltas() <-chan Delta {
	if observation == nil || observation.subscription == nil {
		return closedDeltaStream
	}
	return observation.subscription.Deltas()
}

// Tailing reports whether this observation allocated a PendingHub watcher.
func (observation *InteractionObservation) Tailing() bool {
	return observation != nil && observation.subscription != nil
}

// Context is canceled by the caller, reconnect fencing, or daemon shutdown.
func (observation *InteractionObservation) Context() context.Context {
	if observation == nil || observation.ctx == nil {
		return canceledObservationContext()
	}
	return observation.ctx
}

// Wait reports tail termination. Finite snapshot observations return nil
// immediately and still retain their lease until Close.
func (observation *InteractionObservation) Wait(ctx context.Context) error {
	if observation == nil {
		return errors.New("interaction observation is nil")
	}
	if ctx == nil {
		return errors.New("interaction observation wait context is nil")
	}
	if observation.subscription == nil {
		return nil
	}
	return observation.subscription.Wait(ctx)
}

// Close joins internal pending delivery before releasing the session lease.
func (observation *InteractionObservation) Close() {
	if observation == nil {
		return
	}
	observation.close.Do(func() {
		if observation.cancel != nil {
			observation.cancel()
		}
		if observation.subscription != nil {
			_ = observation.subscription.Wait(context.Background())
		}
		observation.lease.Close()
	})
}

// ReplayEvents acquires one reconnect fence and returns an owned observation
// for a bounded, gap-free run page and optional atomic tail.
func (host *RunHost) ReplayEvents(
	ctx context.Context,
	session Session,
	run client.RunRef,
	request event.ReplayRequest,
) (*EventObservation, error) {
	if host == nil {
		return nil, ErrRunHostClosed
	}
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := run.Validate(); err != nil {
		return nil, ErrHostedRunUnavailable
	}
	if !host.validReplayRequest(request) {
		return nil, ErrRunHostState
	}
	if err := host.beginOperation(); err != nil {
		return nil, err
	}
	defer host.endOperation()

	lease, err := host.sessions.acquireStream(
		ctx, session.ClientID(), session.Epoch(), int(host.limits.ConcurrentStreams()),
	)
	if err != nil {
		return nil, publicRunHostError(err)
	}
	target, err := host.ownedEventRun(session.ClientID(), run.ID())
	if err != nil {
		lease.Close()
		return nil, err
	}
	streamContext, cancelStream := mergeContexts(lease.Context(), host.root)
	page, err := target.ReplayEvents(streamContext, request)
	if err != nil {
		cancelStream()
		lease.Close()
		return nil, publicReplayError(err)
	}
	if page.Tailing != (page.Tail != nil) {
		cancelStream()
		if page.Tail != nil {
			_ = page.Tail.Wait(context.Background())
		}
		lease.Close()
		return nil, ErrRunHostUnavailable
	}
	return &EventObservation{
		page: page, ctx: streamContext, cancel: cancelStream, lease: lease,
	}, nil
}

// SubscribeInteractions returns a complete client-scoped snapshot plus a live
// revision-contiguous tail under one owned reconnect fence.
func (host *RunHost) SubscribeInteractions(
	ctx context.Context,
	session Session,
) (*InteractionObservation, error) {
	return host.observeInteractions(ctx, session, true)
}

// SnapshotInteractions returns one finite complete snapshot under a reconnect
// fence without allocating a PendingHub watcher. The caller releases the fence
// only after delivering the snapshot by calling Close.
func (host *RunHost) SnapshotInteractions(
	ctx context.Context,
	session Session,
) (*InteractionObservation, error) {
	return host.observeInteractions(ctx, session, false)
}

func (host *RunHost) observeInteractions(
	ctx context.Context,
	session Session,
	tail bool,
) (*InteractionObservation, error) {
	if host == nil {
		return nil, ErrRunHostClosed
	}
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := host.beginOperation(); err != nil {
		return nil, err
	}
	defer host.endOperation()

	lease, err := host.sessions.acquireStream(
		ctx, session.ClientID(), session.Epoch(), int(host.limits.ConcurrentStreams()),
	)
	if err != nil {
		return nil, publicRunHostError(err)
	}
	streamContext, cancelStream := mergeContexts(lease.Context(), host.root)
	observation := &InteractionObservation{
		ctx: streamContext, cancel: cancelStream, lease: lease,
	}
	if !tail {
		observation.snapshot, err = host.pending.Snapshot(session.ClientID())
	} else {
		observation.subscription, err = host.pending.Subscribe(streamContext, session.ClientID())
		if err == nil {
			observation.snapshot = observation.subscription.Snapshot()
		}
	}
	if err == nil {
		err = streamContext.Err()
	}
	if err != nil {
		cancelStream()
		if observation.subscription != nil {
			_ = observation.subscription.Wait(context.Background())
		}
		lease.Close()
		return nil, publicRunHostError(err)
	}
	return observation, nil
}

func (host *RunHost) validReplayRequest(request event.ReplayRequest) bool {
	return request.MaxEvents > 0 && uint64(request.MaxEvents) <= uint64(host.limits.ReplayEvents()) &&
		request.MaxBytes > 0 && uint64(request.MaxBytes) <= host.limits.ReplayBytes()
}

func (host *RunHost) ownedEventRun(clientID, runID string) (*agent.Run, error) {
	active, terminal, err := host.ownedRun(clientID, runID)
	if err != nil {
		return nil, err
	}
	if active != nil && active.run != nil {
		return active.run, nil
	}
	if terminal != nil && terminal.run != nil {
		return terminal.run, nil
	}
	return nil, ErrRunHostUnavailable
}

func publicReplayError(err error) error {
	if _, ok := errors.AsType[*event.OutOfRangeError](err); ok {
		return err
	}
	if _, ok := errors.AsType[*event.ResourceExhaustedError](err); ok {
		return err
	}
	return publicRunHostError(err)
}
