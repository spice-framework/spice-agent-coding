package daemon

import (
	"path/filepath"
	"testing"
)

func TestNewCodingConfigCanonicalizesWorkspaceAndRequiresRegistry(t *testing.T) {
	t.Parallel()
	workspace := filepath.Join("relative", "workspace", "..", "root")
	config, err := NewCodingConfig(Properties{Workspace: workspace}, &rootRegistryFixture{})
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
	if _, err = NewCodingConfig(Properties{Workspace: workspace}, nil); err == nil {
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
