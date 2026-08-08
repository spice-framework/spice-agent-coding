//go:build spice_acceptance && !spice_generate

package main

import (
	"errors"
	"os"

	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agent"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	spicebean "github.com/spice-framework/spice/bean"
)

func acceptanceApplicationOptions(options spicegen.ApplicationOptions) (spicegen.ApplicationOptions, error) {
	directory := os.Getenv("SPICE_AGENT_ACCEPTANCE_SCOPE_DIRECTORY")
	if directory == "" {
		return spicegen.ApplicationOptions{}, errors.New("acceptance endpoint scope directory is required")
	}
	scope, err := endpoint.AcceptanceUserScope(directory)
	if err != nil {
		return spicegen.ApplicationOptions{}, err
	}
	options.Overrides.TerminalEndpointScope = spicebean.Replace(scope)
	return options, nil
}
