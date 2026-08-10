//go:build !spice_acceptance && !spice_generate

package main

import spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agent"

type acceptanceAdapter struct{}

func (acceptanceAdapter) applicationOptions(
	options spicegen.ApplicationOptions,
) (spicegen.ApplicationOptions, error) {
	return options, nil
}
