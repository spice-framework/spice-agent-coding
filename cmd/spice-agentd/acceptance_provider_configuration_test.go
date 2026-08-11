//go:build spice_acceptance && !spice_generate

package main

import (
	"path/filepath"
	"testing"

	"github.com/spice-framework/spice-agent-coding/internal/daemon"
	"github.com/spice-framework/spice-agent/model"
)

func TestAcceptanceProviderConfigurationPreservesValidationContract(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	shellHelper := filepath.Join(t.TempDir(), "shell-helper")
	uncleanShellHelper := shellHelper + string(filepath.Separator) + ".." +
		string(filepath.Separator) + filepath.Base(shellHelper)
	tests := map[string]struct {
		configuration acceptanceProviderConfiguration
		wantValid     bool
	}{
		"response stream": {
			configuration: acceptanceProviderConfiguration{directory: directory, prefix: "response"},
			wantValid:     true,
		},
		"provider cancellation": {
			configuration: acceptanceProviderConfiguration{directory: directory, scenario: acceptanceScenarioProvider},
			wantValid:     true,
		},
		"plugin cancellation": {
			configuration: acceptanceProviderConfiguration{directory: directory, scenario: acceptanceScenarioPlugin},
			wantValid:     true,
		},
		"shell cancellation": {
			configuration: acceptanceProviderConfiguration{
				directory: directory, scenario: acceptanceScenarioShell, shellHelper: shellHelper,
			},
			wantValid: true,
		},
		"relative directory": {
			configuration: acceptanceProviderConfiguration{directory: "relative", prefix: "response"},
		},
		"trimmed prefix required": {
			configuration: acceptanceProviderConfiguration{directory: directory, prefix: " response"},
		},
		"trimmed scenario required": {
			configuration: acceptanceProviderConfiguration{directory: directory, scenario: acceptanceScenario(" provider")},
		},
		"response prefix required": {
			configuration: acceptanceProviderConfiguration{directory: directory},
		},
		"cancellation excludes prefix": {
			configuration: acceptanceProviderConfiguration{
				directory: directory, prefix: "response", scenario: acceptanceScenarioProvider,
			},
		},
		"shell helper must be absolute": {
			configuration: acceptanceProviderConfiguration{
				directory: directory, scenario: acceptanceScenarioShell, shellHelper: "relative",
			},
		},
		"shell helper must be clean": {
			configuration: acceptanceProviderConfiguration{
				directory: directory, scenario: acceptanceScenarioShell, shellHelper: uncleanShellHelper,
			},
		},
		"unknown scenario": {
			configuration: acceptanceProviderConfiguration{directory: directory, scenario: acceptanceScenario("unknown")},
		},
	}

	for name, current := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := current.configuration.valid(); got != current.wantValid {
				t.Fatalf("valid = %t, want %t", got, current.wantValid)
			}
		})
	}
}

func TestAcceptanceEnvironmentPreservesHarnessProtocol(t *testing.T) {
	environment := acceptanceEnvironment{}
	t.Setenv(acceptanceEnvironmentScopeDirectory, "scope")
	t.Setenv(acceptanceEnvironmentFaultTrigger, "trigger")
	t.Setenv(acceptanceEnvironmentFaultAcknowledgement, "ack")
	t.Setenv(acceptanceEnvironmentDiagnosticPath, "diagnostic")
	t.Setenv(acceptanceEnvironmentProviderDirectory, "provider")
	t.Setenv(acceptanceEnvironmentResponsePrefix, "response")
	t.Setenv(acceptanceEnvironmentCancellationScenario, "plugin")
	t.Setenv(acceptanceEnvironmentShellHelper, "shell")

	if environment.scopeDirectory() != "scope" || environment.faultTrigger() != "trigger" ||
		environment.faultAcknowledgement() != "ack" || environment.diagnosticPath() != "diagnostic" {
		t.Fatal("acceptance process environment protocol changed")
	}
	configuration := environment.providerConfiguration()
	if configuration.directory != "provider" || configuration.prefix != "response" ||
		configuration.scenario != acceptanceScenarioPlugin || configuration.shellHelper != "shell" {
		t.Fatal("acceptance provider environment protocol changed")
	}
}

func TestAcceptanceTypesSatisfyRuntimeInterfacesWithoutPackageAssertions(t *testing.T) {
	t.Parallel()

	var listenerFactory daemon.ListenerFactory = &faultingListenerFactory{}
	var provider model.Provider = newAcceptanceProvider(acceptanceProviderConfiguration{})
	streams := []model.Stream{
		&acceptanceStream{},
		newBlockingAcceptanceStream(t.TempDir()),
	}
	if listenerFactory == nil || provider == nil || len(streams) != 2 {
		t.Fatal("acceptance runtime interface assignment failed")
	}
}
