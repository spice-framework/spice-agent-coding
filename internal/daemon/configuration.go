package daemon

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spice-framework/spice-agent-coding/internal/daemonprocess"
	openaiprovider "github.com/spice-framework/spice-agent-provider-openai"
	coding "github.com/spice-framework/spice-agent-tools-coding"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// NewOpenAIConfig adapts generated secret-aware properties to the provider
// starter's exact typed configuration bean.
//
// @Bean(name="openAIConfig")
func NewOpenAIConfig(properties Properties) openaiprovider.Config {
	return openaiprovider.Config{
		APIKey:       properties.APIKey,
		BaseURL:      properties.BaseURL,
		Organization: properties.Organization,
		Project:      properties.Project,
		Timeout:      properties.ProviderTimeout,
		MaxRetries:   properties.ProviderRetries,
	}
}

// NewCodingConfig selects one canonical application-owned workspace for every
// compiled coding tool.
//
// @Bean(name="codingConfig")
func NewCodingConfig(
	properties Properties,
	registry daemonprocess.RootRegistry,
) (coding.Config, error) {
	if registry == nil {
		return coding.Config{}, errors.New("daemon root registry is unavailable")
	}
	root, err := filepath.Abs(properties.Workspace)
	if err != nil {
		return coding.Config{}, fmt.Errorf("resolve coding workspace: %w", err)
	}
	return coding.Config{Root: filepath.Clean(root)}, nil
}
