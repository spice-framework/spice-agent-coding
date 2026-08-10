package tuisession

import (
	"errors"
	"sync"

	"github.com/spice-framework/spice-agent/client"
)

type sessionResources struct {
	owner             *Session
	streamMutex       sync.Mutex
	eventStream       client.EventStream
	interactionStream client.InteractionStream
	workersMutex      sync.Mutex
	workersClosed     bool
	workers           sync.WaitGroup
}

func (session *sessionResources) startWorker(work func()) {
	session.owner.workersMutex.Lock()
	defer session.owner.workersMutex.Unlock()
	if session.owner.workersClosed {
		return
	}
	session.owner.workers.Go(work)
}

func (session *sessionResources) releaseEventStream(stream client.EventStream) error {
	session.owner.streamMutex.Lock()
	if session.owner.eventStream == stream {
		session.owner.eventStream = nil
	}
	session.owner.streamMutex.Unlock()
	return stream.Close()
}

func (session *sessionResources) releaseInteractionStream(stream client.InteractionStream) error {
	session.owner.streamMutex.Lock()
	if session.owner.interactionStream == stream {
		session.owner.interactionStream = nil
	}
	session.owner.streamMutex.Unlock()
	return stream.Close()
}

func (session *sessionResources) closeStreams() error {
	session.owner.streamMutex.Lock()
	eventStream := session.owner.eventStream
	interactionStream := session.owner.interactionStream
	session.owner.eventStream = nil
	session.owner.interactionStream = nil
	session.owner.streamMutex.Unlock()
	var eventErr, interactionErr error
	if eventStream != nil {
		eventErr = eventStream.Close()
	}
	if interactionStream != nil {
		interactionErr = interactionStream.Close()
	}
	return errors.Join(eventErr, interactionErr)
}
