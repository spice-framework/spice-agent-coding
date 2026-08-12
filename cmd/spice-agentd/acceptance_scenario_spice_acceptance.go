//go:build spice_acceptance && !spice_generate

package main

type acceptanceScenario string

const (
	acceptanceScenarioNone     acceptanceScenario = ""
	acceptanceScenarioProvider acceptanceScenario = "provider"
	acceptanceScenarioShell    acceptanceScenario = "shell"
	acceptanceScenarioPlugin   acceptanceScenario = "plugin"
)
