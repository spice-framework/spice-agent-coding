package tuisession

import (
	"context"
	"errors"
	"sync"

	"github.com/spice-framework/spice-agent/client"
)

type sessionLifecycle struct {
	owner       *Session
	closed      chan struct{}
	closeOnce   sync.Once
	closeDone   chan struct{}
	closeResult error
}

// Close stops observation, closes the negotiated client session, and waits for
// adapter workers. It is safe for concurrent and repeated lifecycle cleanup.
func (session *sessionLifecycle) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("close TUI session: context must not be nil")
	}
	session.owner.closeOnce.Do(func() { go session.owner.close() })
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-session.owner.closeDone:
		return session.owner.closeResult
	}
}

func (session *sessionLifecycle) close() {
	defer close(session.owner.closeDone)
	close(session.owner.closed)
	session.owner.initializeOnce.Do(func() {
		session.owner.clientMutex.Lock()
		session.owner.initializeErr = client.ErrClosed
		session.owner.clientMutex.Unlock()
		close(session.owner.initializeDone)
	})
	session.owner.workersMutex.Lock()
	session.owner.workersClosed = true
	session.owner.workersMutex.Unlock()
	streamErr := session.owner.closeStreams()
	<-session.owner.initializeDone
	clientSession := session.owner.currentClient()
	var clientErr error
	if clientSession != nil {
		clientErr = clientSession.Close()
	}
	session.owner.workers.Wait()
	session.owner.closeResult = errors.Join(streamErr, clientErr)
}
