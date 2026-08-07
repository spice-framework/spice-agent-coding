package agenttui

import "github.com/spice-framework/spice/starter"

// Manifest returns immutable compatibility metadata for the UI contracts,
// public terminal facade, and explicitly selected auto-configuration.
func Manifest() starter.Manifest {
	return starter.Must(starter.Spec{
		Schema:    starter.Schema,
		ID:        "github.com/spice-framework/spice-agent-tui",
		Version:   "0.1.0-dev",
		Module:    "github.com/spice-framework/spice-agent-tui",
		SpiceAPI:  starter.APIVersion,
		MinimumGo: "1.26.5",
		License:   "Apache-2.0",
		Review:    "docs/dependency-review.md",
		Activation: starter.Activation{
			Mode: starter.ActivationExplicitConstructor,
			EntryPoints: []starter.EntryPoint{
				{
					Package: "github.com/spice-framework/spice-agent-tui/terminal",
					Symbol:  "NewFixedRenderer",
				},
				{
					Package: "github.com/spice-framework/spice-agent-tui/terminal",
					Symbol:  "NewShell",
				},
			},
		},
		Capabilities: []string{
			"agent.tui.annotations",
			"agent.tui.autoconfigure",
			"agent.tui.contracts",
			"agent.tui.presentation",
			"agent.tui.session",
		},
		Dependencies: []starter.Dependency{
			{Module: "charm.land/bubbletea/v2", Version: "v2.0.8", License: "MIT"},
			{Module: "github.com/charmbracelet/x/ansi", Version: "v0.11.7", License: "MIT"},
		},
	})
}
