//go:build !spice_generate

package main

import (
	"context"

	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
)

type applicationFactory func(
	context.Context,
	spicegen.ApplicationOptions,
) (daemonApplication, error)
