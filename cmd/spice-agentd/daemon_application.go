//go:build !spice_generate

package main

import (
	"context"
	"time"
)

type daemonApplication interface {
	Start(context.Context) error
	Stop(context.Context) error
	ShutdownTimeout() time.Duration
	RuntimeDone() <-chan struct{}
	RuntimeErr() error
}
