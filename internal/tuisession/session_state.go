package tuisession

import (
	"errors"
	"fmt"
	"sync"

	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent/client"
)

type sessionState struct {
	owner               *Session
	stateMutex          sync.Mutex
	activeRun           client.RunRef
	hasActiveRun        bool
	eventCursor         uint64
	promptHistory       []agenttui.Text
	pending             map[interactionKey]client.PendingInteraction
	interactionRevision uint64
}

func (session *sessionState) handleInteractionUpdate(
	update client.InteractionUpdate,
	wantSnapshot *bool,
) error {
	if *wantSnapshot && update.Kind() != client.InteractionSnapshot {
		return errors.New("interaction stream did not begin with a complete snapshot")
	}
	if !*wantSnapshot && update.Kind() == client.InteractionSnapshot {
		return errors.New("interaction stream returned a second snapshot")
	}
	*wantSnapshot = false
	changed, err := session.owner.mergeInteraction(update)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return session.owner.publishCurrentInteraction()
}

func (session *sessionState) mergeInteraction(update client.InteractionUpdate) (bool, error) {
	session.owner.stateMutex.Lock()
	defer session.owner.stateMutex.Unlock()
	previous, hadPrevious := (interactionSelector{}).selectCurrent(session.owner.pending, session.owner.activeRun, session.owner.hasActiveRun)
	switch update.Kind() {
	case client.InteractionSnapshot:
		values, available := update.Snapshot()
		if !available {
			return false, errors.New("interaction snapshot has no snapshot payload")
		}
		if update.Revision() < session.owner.interactionRevision {
			return false, fmt.Errorf(
				"interaction snapshot revision moved backwards from %d to %d",
				session.owner.interactionRevision, update.Revision(),
			)
		}
		next := make(map[interactionKey]client.PendingInteraction, len(values))
		for _, value := range values {
			next[interactionKey{run: value.Run().ID(), id: value.ID()}] = value
		}
		session.owner.pending = next
	case client.InteractionOpened, client.InteractionClosed:
		if update.Revision() != session.owner.interactionRevision+1 {
			return false, fmt.Errorf(
				"interaction revision is not contiguous: expected %d, received %d",
				session.owner.interactionRevision+1, update.Revision(),
			)
		}
		item, available := update.Item()
		if !available {
			return false, errors.New("interaction change has no item payload")
		}
		key := interactionKey{run: item.Run().ID(), id: item.ID()}
		if update.Kind() == client.InteractionOpened {
			if _, exists := session.owner.pending[key]; exists {
				return false, fmt.Errorf("interaction %s/%s opened twice", key.run, key.id)
			}
			session.owner.pending[key] = item
		} else {
			if _, exists := session.owner.pending[key]; !exists {
				return false, fmt.Errorf("interaction %s/%s closed before opening", key.run, key.id)
			}
			delete(session.owner.pending, key)
		}
	default:
		return false, fmt.Errorf("unsupported interaction update kind %q", update.Kind())
	}
	session.owner.interactionRevision = update.Revision()
	current, hasCurrent := (interactionSelector{}).selectCurrent(session.owner.pending, session.owner.activeRun, session.owner.hasActiveRun)
	return !(interactionSelector{}).same(previous, hadPrevious, current, hasCurrent), nil
}

func (session *sessionState) publishCurrentInteraction() error {
	pending, found := session.owner.currentInteraction()
	if !found {
		return session.owner.publishActivity("interaction resolved")
	}
	return session.owner.publishActivity("interaction: " + pending.Prompt())
}

func (session *sessionState) currentInteraction() (client.PendingInteraction, bool) {
	session.owner.stateMutex.Lock()
	defer session.owner.stateMutex.Unlock()
	return (interactionSelector{}).selectCurrent(session.owner.pending, session.owner.activeRun, session.owner.hasActiveRun)
}

func (session *sessionState) currentInteractionRevision() uint64 {
	session.owner.stateMutex.Lock()
	defer session.owner.stateMutex.Unlock()
	return session.owner.interactionRevision
}

func (session *sessionState) clearActiveRun(run client.RunRef) {
	session.owner.stateMutex.Lock()
	defer session.owner.stateMutex.Unlock()
	if session.owner.hasActiveRun && session.owner.activeRun.ID() == run.ID() {
		session.owner.activeRun = client.RunRef{}
		session.owner.hasActiveRun = false
		session.owner.eventCursor = 0
	}
}
