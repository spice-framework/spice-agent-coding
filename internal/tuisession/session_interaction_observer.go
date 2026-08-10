package tuisession

import (
	"errors"
	"fmt"

	"github.com/spice-framework/spice-agent/client"
)

type sessionInteractionObserver struct{ owner *Session }

func (session *sessionInteractionObserver) observeInteractions() {
	for {
		stream, generation, err := session.owner.openInteractionStream()
		if err != nil {
			reconnecting := false
			if session.owner.retryObservation("interaction", 0, generation, err, &reconnecting) {
				continue
			}
			session.owner.recordFailure(fmt.Errorf("open interaction stream: %w", err))
			return
		}
		err = session.owner.consumeInteractionStream(stream)
		closeErr := session.owner.releaseInteractionStream(stream)
		if closeErr != nil && err == nil {
			err = fmt.Errorf("close interaction stream: %w", closeErr)
		}
		if err != nil && !session.owner.canRetryObservation(err) {
			session.owner.recordFailure(fmt.Errorf("observe interactions: %w", err))
			return
		}
		reconnecting := false
		if !session.owner.retryObservation("interaction", 0, generation, err, &reconnecting) {
			return
		}
	}
}

func (session *sessionInteractionObserver) openInteractionStream() (client.InteractionStream, uint64, error) {
	clientSession, generation := session.owner.currentClientGeneration()
	stream, err := clientSession.Interactions(
		session.owner.context(),
		client.NewInteractionStreamOptions(true),
	)
	if err != nil {
		return nil, generation, err
	}
	if stream == nil {
		return nil, generation, errors.New("client returned a nil interaction stream")
	}
	session.owner.streamMutex.Lock()
	session.owner.interactionStream = stream
	session.owner.streamMutex.Unlock()
	return stream, generation, nil
}

func (session *sessionInteractionObserver) consumeInteractionStream(stream client.InteractionStream) error {
	wantSnapshot := true
	for {
		frame, err := stream.Next(session.owner.context())
		if err != nil {
			return err
		}
		if err = session.owner.handleInteractionFrame(frame, &wantSnapshot); err != nil {
			return err
		}
	}
}

func (session *sessionInteractionObserver) handleInteractionFrame(
	frame client.InteractionFrame,
	wantSnapshot *bool,
) error {
	switch frame.Kind() {
	case client.InteractionFrameUpdate:
		update, available := frame.Update()
		if !available {
			return errors.New("interaction frame has no update payload")
		}
		return session.owner.handleInteractionUpdate(update, wantSnapshot)
	case client.InteractionFrameControl:
		if *wantSnapshot {
			return errors.New("interaction stream control arrived before its snapshot")
		}
		control, available := frame.Control()
		if !available {
			return errors.New("interaction frame has no control payload")
		}
		if control.PageLastRevision() != session.owner.currentInteractionRevision() {
			return fmt.Errorf(
				"interaction control revision %d does not match merged revision %d",
				control.PageLastRevision(), session.owner.currentInteractionRevision(),
			)
		}
		return nil
	case client.InteractionFrameKind(""):
		return errors.New("interaction stream returned an empty frame")
	default:
		return fmt.Errorf("interaction stream returned unsupported frame kind %q", frame.Kind())
	}
}
