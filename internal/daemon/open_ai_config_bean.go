package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	openaiprovider "github.com/spice-framework/spice-agent-provider-openai"
)

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
