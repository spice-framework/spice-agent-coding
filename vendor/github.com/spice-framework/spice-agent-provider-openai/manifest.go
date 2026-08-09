package openaiprovider

import spicestarter "github.com/spice-framework/spice/annotation/sdk/starter"

// Manifest returns OpenAI provider compatibility and review metadata.
func Manifest() spicestarter.Manifest {
	return spicestarter.Must(spicestarter.Spec{
		Schema:    spicestarter.Schema,
		ID:        "github.com/spice-framework/spice-agent-provider-openai",
		Version:   "0.1.0-dev",
		Module:    "github.com/spice-framework/spice-agent-provider-openai",
		SpiceAPI:  spicestarter.APIVersion,
		MinimumGo: "1.26.5",
		License:   "Apache-2.0",
		Review:    "docs/dependency-review.md",
		Activation: spicestarter.Activation{
			Mode: spicestarter.ActivationExplicitConstructor,
			EntryPoints: []spicestarter.EntryPoint{{
				Package: "github.com/spice-framework/spice-agent-provider-openai",
				Symbol:  "New",
			}},
		},
		Capabilities: []string{"agent.model.openai"},
		Dependencies: []spicestarter.Dependency{
			{
				Module:  "github.com/spice-framework/spice-agent",
				Version: "v0.1.0-preview.4",
				License: "Apache-2.0",
			},
			{
				Module:  "github.com/openai/openai-go/v3",
				Version: "v3.50.0",
				License: "Apache-2.0",
			},
		},
	})
}
