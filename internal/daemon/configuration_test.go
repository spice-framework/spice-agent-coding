package daemon

import (
	"path/filepath"
	"testing"

	workspaceconfig "github.com/spice-framework/spice-agent-coding/internal/workspace"
)

func TestNewAgentLoggingConfigMapsValidatedDistributionProperties(t *testing.T) {
	t.Parallel()
	config, err := NewAgentLoggingConfig(Properties{
		LoggingMailboxCapacity: 4096,
		LoggingIncludeProgress: true,
		LoggingReadinessImpact: true,
	})
	if err != nil {
		t.Fatalf("NewAgentLoggingConfig() error = %v", err)
	}
	if config.MailboxCapacity != 4096 || !config.IncludeProgress || !config.ReadinessImpact {
		t.Fatalf("Agent logging config = %#v", config)
	}
	for _, capacity := range []int{0, 65537} {
		if _, configErr := NewAgentLoggingConfig(Properties{LoggingMailboxCapacity: capacity}); configErr == nil {
			t.Fatalf("NewAgentLoggingConfig() accepted capacity %d", capacity)
		}
	}
}

func TestNewCodingConfigCanonicalizesWorkspaceAndRequiresRegistry(t *testing.T) {
	t.Parallel()
	workspace := filepath.Join("relative", "workspace", "..", "root")
	config, err := NewCodingConfig(workspaceconfig.Properties{Workspace: workspace}, &rootRegistryFixture{})
	if err != nil {
		t.Fatalf("NewCodingConfig() error = %v", err)
	}
	want, err := filepath.Abs(workspace)
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}
	if config.Root != filepath.Clean(want) {
		t.Fatalf("root = %q, want %q", config.Root, filepath.Clean(want))
	}
	if _, err = NewCodingConfig(workspaceconfig.Properties{Workspace: workspace}, nil); err == nil {
		t.Fatal("NewCodingConfig() accepted a nil registry")
	}
}

func TestNewOpenAIConfigPreservesTypedProperties(t *testing.T) {
	t.Parallel()
	properties := Properties{
		APIKey: "sensitive", BaseURL: "https://example.invalid/v1",
		Organization: "organization", Project: "project",
		ProviderTimeout: 42, ProviderRetries: 3,
	}
	config := NewOpenAIConfig(properties)
	if config.APIKey != properties.APIKey || config.BaseURL != properties.BaseURL ||
		config.Organization != properties.Organization || config.Project != properties.Project ||
		config.Timeout != properties.ProviderTimeout || config.MaxRetries != properties.ProviderRetries {
		t.Fatal("provider configuration did not preserve typed properties")
	}
}
