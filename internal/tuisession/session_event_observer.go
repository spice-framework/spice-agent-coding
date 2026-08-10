package tuisession

import (
	"errors"
	"fmt"

	"github.com/spice-framework/spice-agent/client"
)

type sessionEventObserver struct{ owner *Session }

func (session *sessionEventObserver) observeEvents(run client.RunRef) {
	cursor := uint64(0)
	reconnecting := false
	for {
		stream, generation, err := session.owner.openEventStream(run, cursor)
		if err != nil {
			if session.owner.retryObservation("event", cursor, generation, err, &reconnecting) {
				continue
			}
			session.owner.recordFailure(fmt.Errorf("open event stream for run %s: %w", run.ID(), err))
			return
		}
		if reconnecting {
			if err = session.owner.publishActivity(fmt.Sprintf(
				"event stream reconnected after sequence %d (replay limit %d)", cursor, session.owner.config.ReplayLimit,
			)); err != nil {
				releaseErr := session.owner.releaseEventStream(stream)
				session.owner.recordFailure(errors.Join(
					fmt.Errorf("publish event reconnection: %w", err),
					releaseErr,
				))
				return
			}
			reconnecting = false
		}
		terminal, nextCursor, err := session.owner.consumeEventStream(run, cursor, stream)
		closeErr := session.owner.releaseEventStream(stream)
		if closeErr != nil && err == nil {
			err = fmt.Errorf("close event stream for run %s: %w", run.ID(), closeErr)
		}
		cursor = nextCursor
		if terminal {
			session.owner.clearActiveRun(run)
			return
		}
		if err == nil {
			continue
		}
		if !session.owner.canRetryObservation(err) {
			session.owner.recordFailure(fmt.Errorf("observe events for run %s after sequence %d: %w", run.ID(), cursor, err))
			return
		}
		if !session.owner.retryObservation("event", cursor, generation, err, &reconnecting) {
			return
		}
	}
}

func (session *sessionEventObserver) openEventStream(run client.RunRef, after uint64) (client.EventStream, uint64, error) {
	cursor, err := client.NewCursor(run, after)
	if err != nil {
		return nil, 0, err
	}
	clientSession, generation := session.owner.currentClientGeneration()
	options, err := client.NewEventStreamOptions(
		session.owner.config.ReplayLimit,
		true,
		clientSession.Connection().Limits(),
	)
	if err != nil {
		return nil, generation, err
	}
	stream, err := clientSession.Events(session.owner.context(), cursor, options)
	if err != nil {
		return nil, generation, err
	}
	if stream == nil {
		return nil, generation, errors.New("client returned a nil event stream")
	}
	session.owner.streamMutex.Lock()
	session.owner.eventStream = stream
	session.owner.streamMutex.Unlock()
	return stream, generation, nil
}

func (session *sessionEventObserver) consumeEventStream(
	run client.RunRef,
	after uint64,
	stream client.EventStream,
) (bool, uint64, error) {
	cursor := after
	for {
		frame, err := stream.Next(session.owner.context())
		if err != nil {
			return false, cursor, err
		}
		terminal, pageComplete, nextCursor, handleErr := session.owner.handleEventFrame(run, cursor, frame)
		if handleErr != nil {
			return false, cursor, handleErr
		}
		cursor = nextCursor
		if terminal || pageComplete {
			return terminal, cursor, nil
		}
	}
}

func (session *sessionEventObserver) handleEventFrame(
	run client.RunRef,
	cursor uint64,
	frame client.EventFrame,
) (bool, bool, uint64, error) {
	switch frame.Kind() {
	case client.EventFrameEvent:
		event, available := frame.Event()
		if !available {
			return false, false, cursor, errors.New("event frame has no event payload")
		}
		nextCursor, err := session.owner.handleEvent(run, cursor, event)
		return (eventPresentation{}).runTerminal(event.Kind()), false, nextCursor, err
	case client.EventFrameControl:
		control, available := frame.Control()
		if !available {
			return false, false, cursor, errors.New("event control frame has no control payload")
		}
		if control.LastDeliveredSequence() != cursor {
			return false, false, cursor, fmt.Errorf(
				"event control acknowledged sequence %d, want %d",
				control.LastDeliveredSequence(), cursor,
			)
		}
		return false, control.HasMore(), cursor, nil
	case client.EventFrameKind(""):
		return false, false, cursor, errors.New("event stream returned an empty frame")
	default:
		return false, false, cursor, fmt.Errorf("event stream returned unsupported frame kind %q", frame.Kind())
	}
}

func (session *sessionEventObserver) handleEvent(
	run client.RunRef,
	cursor uint64,
	event client.Event,
) (uint64, error) {
	if event.Run().ID() != run.ID() || event.Sequence() != cursor+1 {
		return cursor, fmt.Errorf(
			"event sequence is not contiguous: expected %s/%d, received %s/%d",
			run.ID(), cursor+1, event.Run().ID(), event.Sequence(),
		)
	}
	if err := session.owner.publishActivity((eventPresentation{}).summary(event)); err != nil {
		return cursor, err
	}
	cursor = event.Sequence()
	session.owner.stateMutex.Lock()
	if session.owner.hasActiveRun && session.owner.activeRun.ID() == run.ID() {
		session.owner.eventCursor = cursor
	}
	session.owner.stateMutex.Unlock()
	return cursor, nil
}
