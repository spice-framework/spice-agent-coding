package tuisession

import (
	"context"
	"time"
)

type closeContext struct{ done <-chan struct{} }

func (closeContext) Deadline() (time.Time, bool)   { return time.Time{}, false }
func (current closeContext) Done() <-chan struct{} { return current.done }
func (current closeContext) Err() error {
	select {
	case <-current.done:
		return context.Canceled
	default:
		return nil
	}
}
func (closeContext) Value(any) any { return nil }
