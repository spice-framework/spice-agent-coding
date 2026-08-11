//go:build spice_acceptance && !spice_generate

package main

import "os"

const (
	acceptanceEnvironmentScopeDirectory       = "SPICE_AGENT_ACCEPTANCE_SCOPE_DIRECTORY"
	acceptanceEnvironmentFaultTrigger         = "SPICE_AGENT_ACCEPTANCE_FAULT_TRIGGER"
	acceptanceEnvironmentFaultAcknowledgement = "SPICE_AGENT_ACCEPTANCE_FAULT_ACK"
	acceptanceEnvironmentDiagnosticPath       = "SPICE_AGENT_ACCEPTANCE_DIAGNOSTIC"
	acceptanceEnvironmentProviderDirectory    = "SPICE_AGENT_ACCEPTANCE_PROVIDER_DIRECTORY"
	acceptanceEnvironmentResponsePrefix       = "SPICE_AGENT_ACCEPTANCE_RESPONSE_PREFIX"
	acceptanceEnvironmentCancellationScenario = "SPICE_AGENT_ACCEPTANCE_CANCELLATION_SCENARIO"
	acceptanceEnvironmentShellHelper          = "SPICE_AGENT_ACCEPTANCE_SHELL_HELPER"
)

type acceptanceEnvironment struct{}

func (acceptanceEnvironment) scopeDirectory() string {
	return os.Getenv(acceptanceEnvironmentScopeDirectory)
}

func (acceptanceEnvironment) faultTrigger() string {
	return os.Getenv(acceptanceEnvironmentFaultTrigger)
}

func (acceptanceEnvironment) faultAcknowledgement() string {
	return os.Getenv(acceptanceEnvironmentFaultAcknowledgement)
}

func (acceptanceEnvironment) diagnosticPath() string {
	return os.Getenv(acceptanceEnvironmentDiagnosticPath)
}

func (acceptanceEnvironment) providerConfiguration() acceptanceProviderConfiguration {
	return acceptanceProviderConfiguration{
		directory:   os.Getenv(acceptanceEnvironmentProviderDirectory),
		prefix:      os.Getenv(acceptanceEnvironmentResponsePrefix),
		scenario:    acceptanceScenario(os.Getenv(acceptanceEnvironmentCancellationScenario)),
		shellHelper: os.Getenv(acceptanceEnvironmentShellHelper),
	}
}
