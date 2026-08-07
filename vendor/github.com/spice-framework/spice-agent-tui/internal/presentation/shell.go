package presentation

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
	agenttui "github.com/spice-framework/spice-agent-tui"
)

type bubbleShell struct {
	model    Model
	terminal agenttui.TerminalIO
}

// NewShell constructs a Bubble Tea shell around an already validated model.
// Terminal streams and startup policy are explicit; construction never
// discovers, starts, or attaches to a daemon.
func NewShell(
	model Model,
	terminal agenttui.TerminalIO,
	config agenttui.TerminalConfig,
) (agenttui.Shell, error) {
	if model.renderer == nil {
		return nil, errors.New("shell model must be initialized")
	}
	if err := terminal.Validate(); err != nil {
		return nil, err
	}
	return &bubbleShell{model: model.WithAccessibleMode(config.Accessible()), terminal: terminal}, nil
}

func (shell *bubbleShell) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("shell context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	program := tea.NewProgram(
		shell.model.withEffectsContext(runCtx, cancel),
		tea.WithContext(runCtx),
		tea.WithInput(shell.terminal.Input()),
		tea.WithOutput(shell.terminal.Output()),
	)
	_, err := program.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runCtx.Err() != nil && errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}

var _ agenttui.Shell = (*bubbleShell)(nil)
