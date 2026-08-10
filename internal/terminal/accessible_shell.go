package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"bufio"
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
	agenttui "github.com/spice-framework/spice-agent-tui"
)

const (
	accessibleTerminalWidth  = 120
	accessibleTerminalHeight = 24
)

type accessibleShell struct {
	session agenttui.Session
	initial agenttui.ViewData
	streams agenttui.TerminalIO
}

func (shell *accessibleShell) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("accessible shell context is required")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	model := &accessibleModel{
		context: func() context.Context { return ctx }, session: shell.session,
		workspace: shell.initial.Workspace(), status: shell.initial.Status(),
		activity: shell.initial.Activity(),
	}
	program := tea.NewProgram(
		model,
		tea.WithContext(ctx),
		tea.WithInput(bufio.NewReader(shell.streams.Input())),
		tea.WithOutput(shell.streams.Output()),
		tea.WithWindowSize(accessibleTerminalWidth, accessibleTerminalHeight),
	)
	_, err := program.Run()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}
