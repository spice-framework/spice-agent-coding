//go:build !spice_acceptance && !spice_generate

package main

import (
	"github.com/spice-framework/spice-agent-coding/internal/daemoncommand"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
)

func acceptanceApplicationOptions(options spicegen.ApplicationOptions) (spicegen.ApplicationOptions, error) {
	return options, nil
}

func acceptanceDaemonRunner(runner daemoncommand.Runner) daemoncommand.Runner { return runner }
