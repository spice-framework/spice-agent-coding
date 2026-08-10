//go:build !spice_generate

package main

import (
	"context"
	"time"

	agenttui "github.com/spice-framework/spice-agent-tui"
)

type terminalApplication interface {
	Start(context.Context) error
	Stop(context.Context) error
	ShutdownTimeout() time.Duration
	Shell() agenttui.Shell
}
