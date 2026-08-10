package daemon

import (
	"context"
	"errors"
	"sync"
	"time"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// Root owns the daemon service lifetime independently from request contexts.
type Root struct {
	done chan struct{}
	once sync.Once
}

// Context validates and exposes the application-owned cancellation root.
func (root *Root) Context() (context.Context, error) {
	if root == nil || root.done == nil {
		return nil, errors.New("daemon root is unavailable")
	}
	if err := root.Err(); err != nil {
		return nil, errors.New("daemon root is already canceled")
	}
	return root, nil
}

// Deadline reports that the application root has no implicit wall-clock limit.
func (*Root) Deadline() (time.Time, bool) { return time.Time{}, false }

// Done closes when generated application cleanup cancels the root.
func (root *Root) Done() <-chan struct{} {
	if root == nil {
		return nil
	}
	return root.done
}

// Err reports cancellation without retaining an ambient context value.
func (root *Root) Err() error {
	if root == nil || root.done == nil {
		return nil
	}
	select {
	case <-root.done:
		return context.Canceled
	default:
		return nil
	}
}

// Value deliberately carries no ambient request values into application scope.
func (*Root) Value(any) any { return nil }

func (root *Root) cancel() {
	if root != nil {
		root.once.Do(func() { close(root.done) })
	}
}
