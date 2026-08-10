//go:build !spice_generate

package main

import (
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agent"
	agenttui "github.com/spice-framework/spice-agent-tui"
)

type generatedApplication struct {
	*spicegen.Application
}

func (application generatedApplication) Shell() agenttui.Shell {
	return application.Components().TerminalShell
}
