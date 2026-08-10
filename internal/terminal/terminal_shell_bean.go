package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"errors"
	"fmt"

	agenttui "github.com/spice-framework/spice-agent-tui"
	agentterminal "github.com/spice-framework/spice-agent-tui/terminal"
)

// NewTerminalShell keeps the full upstream interactive shell as the normal
// path and supplies a line-oriented Bubble Tea model for accessibility mode.
// The accessible model fixes an explicit initial canvas so pipes, screen
// readers, and assistive tooling receive semantic output without requiring a
// platform PTY.
//
// @Bean(name="terminalShell")
func NewTerminalShell(
	session agenttui.Session,
	renderer agenttui.Renderer,
	theme agenttui.Theme,
	bindings []agenttui.KeyBinding,
	initial agenttui.ViewData,
	streams agenttui.TerminalIO,
	config agenttui.TerminalConfig,
) (agenttui.Shell, error) {
	if !config.Accessible() {
		return agentterminal.NewShell(session, renderer, theme, bindings, initial, streams, config)
	}
	if session == nil {
		return nil, errors.New("accessible terminal session is required")
	}
	if err := initial.Validate(); err != nil {
		return nil, fmt.Errorf("accessible initial view: %w", err)
	}
	if err := streams.Validate(); err != nil {
		return nil, err
	}
	return &accessibleShell{session: session, initial: initial, streams: streams}, nil
}
