package terminal

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	agenttui "github.com/spice-framework/spice-agent-tui"
)

// NewTerminalConfig selects stable line-oriented presentation when requested
// by typed configuration. The default remains the normal styled terminal.
//
// @Bean(name="terminalConfig")
// @Singleton
func NewTerminalConfig(properties Properties) agenttui.TerminalConfig {
	return agenttui.NewTerminalConfig(properties.TerminalAccessible)
}
