package tuisession

import (
	"context"
	"errors"
	"fmt"
	"sync"

	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent/client"
)

type sessionPublisher struct {
	owner            *Session
	publishMutex     sync.Mutex
	revision         uint64
	updates          chan delivery
	failureMutex     sync.RWMutex
	failure          error
	failureDelivered bool
}

func (session *sessionPublisher) publishSnapshot() error {
	return session.owner.publish(func(revision uint64) (agenttui.SessionUpdate, error) {
		snapshot, err := agenttui.NewSessionSnapshot(
			revision,
			session.owner.config.Workspace,
			session.owner.config.InitialStatus,
			nil,
			nil,
		)
		if err != nil {
			return agenttui.SessionUpdate{}, err
		}
		return agenttui.NewSnapshotUpdate(snapshot)
	})
}

func (session *sessionPublisher) publish(
	build func(uint64) (agenttui.SessionUpdate, error),
) error {
	session.owner.publishMutex.Lock()
	defer session.owner.publishMutex.Unlock()
	if session.owner.revision == ^uint64(0) {
		return errors.New("TUI session revision exhausted")
	}
	nextRevision := session.owner.revision + 1
	update, err := build(nextRevision)
	if err != nil {
		return fmt.Errorf("build TUI update: %w", err)
	}
	select {
	case <-session.owner.closed:
		return client.ErrClosed
	case session.owner.updates <- delivery{update: update}:
		session.owner.revision = nextRevision
		return nil
	}
}

func (session *sessionPublisher) publishActivity(value string) error {
	text, err := (eventPresentation{}).text(value)
	if err != nil {
		return err
	}
	return session.owner.publish(func(revision uint64) (agenttui.SessionUpdate, error) {
		return agenttui.NewActivityUpdate(revision, text)
	})
}

func (session *sessionPublisher) publishHistory(history []agenttui.Text) error {
	return session.owner.publish(func(revision uint64) (agenttui.SessionUpdate, error) {
		return agenttui.NewPromptHistoryUpdate(revision, history)
	})
}

func (session *sessionPublisher) recordFailure(err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, client.ErrClosed) {
		return
	}
	session.owner.failureMutex.Lock()
	if session.owner.failure != nil {
		session.owner.failureMutex.Unlock()
		return
	}
	session.owner.failure = err
	session.owner.failureMutex.Unlock()
	select {
	case <-session.owner.closed:
	case session.owner.updates <- delivery{err: err}:
	}
}

func (session *sessionPublisher) deliveredFailure() error {
	session.owner.failureMutex.RLock()
	defer session.owner.failureMutex.RUnlock()
	if session.owner.failureDelivered {
		return session.owner.failure
	}
	return nil
}

func (session *sessionPublisher) markFailureDelivered() {
	session.owner.failureMutex.Lock()
	session.owner.failureDelivered = true
	session.owner.failureMutex.Unlock()
}
