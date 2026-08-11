//go:build spice_acceptance && !spice_generate

package main

import (
	"path/filepath"
	"strings"
)

type acceptanceProviderConfiguration struct {
	directory   string
	prefix      string
	scenario    acceptanceScenario
	shellHelper string
}

func (configuration acceptanceProviderConfiguration) configured() bool {
	return configuration.directory != "" || configuration.prefix != "" ||
		configuration.scenario != acceptanceScenarioNone || configuration.shellHelper != ""
}

func (configuration acceptanceProviderConfiguration) valid() bool {
	if !filepath.IsAbs(configuration.directory) || strings.TrimSpace(configuration.prefix) != configuration.prefix ||
		strings.TrimSpace(string(configuration.scenario)) != string(configuration.scenario) {
		return false
	}
	switch configuration.scenario {
	case acceptanceScenarioNone:
		return configuration.prefix != "" && configuration.shellHelper == ""
	case acceptanceScenarioProvider, acceptanceScenarioPlugin:
		return configuration.prefix == "" && configuration.shellHelper == ""
	case acceptanceScenarioShell:
		return configuration.prefix == "" && filepath.IsAbs(configuration.shellHelper) &&
			filepath.Clean(configuration.shellHelper) == configuration.shellHelper
	default:
		return false
	}
}
