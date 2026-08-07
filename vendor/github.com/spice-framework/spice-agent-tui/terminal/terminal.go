package terminal

import (
	"errors"
	"fmt"

	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent-tui/internal/presentation"
)

// NewFixedRenderer constructs the deterministic built-in renderer. The
// concrete Bubble Tea implementation remains private to this module.
func NewFixedRenderer() agenttui.Renderer { return presentation.FixedRenderer{} }

// NewShell composes the public UI-neutral contracts into one Bubble Tea shell.
// The session is injected and is never discovered, started, or retried here.
// Theme and key-binding SPI values are validated and snapshotted at
// construction, so later mutable implementation state cannot change a running
// shell's presentation policy.
func NewShell(
	session agenttui.Session,
	renderer agenttui.Renderer,
	theme agenttui.Theme,
	bindings []agenttui.KeyBinding,
	initial agenttui.ViewData,
	streams agenttui.TerminalIO,
	config agenttui.TerminalConfig,
) (agenttui.Shell, error) {
	if err := initial.Validate(); err != nil {
		return nil, fmt.Errorf("initial view: %w", err)
	}
	themeSnapshot, err := snapshotTheme(theme)
	if err != nil {
		return nil, err
	}
	effects, err := presentation.NewSessionEffects(session)
	if err != nil {
		return nil, err
	}
	model, err := presentation.NewModel(
		renderer,
		initial.Workspace(),
		initial.Status(),
		initial.Prompt(),
		initial.Activity(),
		themeSnapshot,
		bindings,
		effects,
	)
	if err != nil {
		return nil, fmt.Errorf("terminal model: %w", err)
	}
	return presentation.NewShell(model, streams, config)
}

func snapshotTheme(theme agenttui.Theme) (snapshot agenttui.ThemeState, returnErr error) {
	if theme == nil {
		return agenttui.ThemeState{}, errors.New("terminal theme must not be nil")
	}
	defer func() {
		if recover() != nil {
			snapshot = agenttui.ThemeState{}
			returnErr = errors.New("terminal theme could not be read")
		}
	}()
	snapshot, err := agenttui.NewTheme(theme.Name(), theme.Mode(), theme.Palette())
	if err != nil {
		return agenttui.ThemeState{}, fmt.Errorf("terminal theme: %w", err)
	}
	return snapshot, nil
}
