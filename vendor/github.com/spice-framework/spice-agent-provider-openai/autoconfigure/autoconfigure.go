// Package autoconfigure contributes the default OpenAI client only when an
// application explicitly blank-imports this package.
package autoconfigure

import (
	provider "github.com/spice-framework/spice-agent-provider-openai"
	"github.com/spice-framework/spice-agent/model"
	"github.com/spice-framework/spice/starter"
)

// Default constructs the fallback instance from application-owned typed
// configuration. It performs no network I/O.
func Default(config provider.Config) (model.Provider, error) {
	return provider.New(config)
}

// SpiceAutoConfiguration is statically decoded by Spice and never executed
// during analysis.
func SpiceAutoConfiguration() starter.AutoConfiguration {
	return starter.AutoConfiguration{
		Review: "docs/dependency-review.md",
		Beans: []starter.AutoBean{{
			Factory:  Default,
			Name:     "openAIModelProvider",
			Fallback: true,
		}},
	}
}
