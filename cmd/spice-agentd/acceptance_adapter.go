//go:build !spice_acceptance && !spice_generate

package main

import (
	"github.com/spice-framework/spice-agent-coding/internal/daemoncommand"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
)

type acceptanceAdapter struct{}

func (acceptanceAdapter) applicationOptions(
	options spicegen.ApplicationOptions,
) (spicegen.ApplicationOptions, error) {
	return options, nil
}

func (acceptanceAdapter) daemonRunner(
	runner daemoncommand.Runner,
) daemoncommand.Runner {
	return runner
}
