package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	agenttui "github.com/spice-framework/spice-agent-tui"
)

// NewTerminalConfig selects stable line-oriented presentation when requested
// by typed configuration. The default remains the normal styled terminal.
//
// @Bean(name="terminalConfig")
func NewTerminalConfig(properties Properties) agenttui.TerminalConfig {
	return agenttui.NewTerminalConfig(properties.TerminalAccessible)
}
