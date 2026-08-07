package daemon

import (
	"context"
	"errors"
)

// runTransition serializes one run's lifecycle boundary while allowing
// request-scoped callers to stop waiting. The terminal monitor deliberately
// uses Lock because its cleanup belongs to the host lifetime, not an RPC.
type runTransition struct {
	token chan struct{}
}

func newRunTransition() *runTransition {
	transition := &runTransition{token: make(chan struct{}, 1)}
	transition.token <- struct{}{}
	return transition
}

func (transition *runTransition) Lock() {
	if transition == nil {
		panic("nil run transition")
	}
	<-transition.token
}

func (transition *runTransition) LockContext(ctx context.Context) error {
	if transition == nil {
		return errors.New("run transition is nil")
	}
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-transition.token:
		if err := ctx.Err(); err != nil {
			transition.Unlock()
			return err
		}
		return nil
	}
}

func (transition *runTransition) TryLock() bool {
	if transition == nil {
		return false
	}
	select {
	case <-transition.token:
		return true
	default:
		return false
	}
}

func (transition *runTransition) Unlock() {
	if transition == nil {
		panic("nil run transition")
	}
	select {
	case transition.token <- struct{}{}:
	default:
		panic("unlock of unlocked run transition")
	}
}
