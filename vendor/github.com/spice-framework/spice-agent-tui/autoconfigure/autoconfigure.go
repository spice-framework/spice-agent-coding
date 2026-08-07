package autoconfigure

import (
	"os"

	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent-tui/terminal"
	"github.com/spice-framework/spice/starter"
)

// DefaultFixedRenderer constructs the replaceable fixed renderer.
func DefaultFixedRenderer() agenttui.Renderer { return terminal.NewFixedRenderer() }

// DefaultDarkTheme returns the replaceable built-in dark theme as the public
// Theme SPI, rather than coupling composition to ThemeState.
func DefaultDarkTheme() agenttui.Theme { return agenttui.DarkTheme() }

// DefaultSubmitBinding contributes the standard submit binding.
func DefaultSubmitBinding() (agenttui.KeyBinding, error) {
	return standardBinding(agenttui.ActionSubmit, []string{"enter"}, "enter submit")
}

// DefaultCancelBinding contributes the standard active-run cancellation binding.
func DefaultCancelBinding() (agenttui.KeyBinding, error) {
	return standardBinding(agenttui.ActionCancelActiveRun, []string{"esc", "ctrl+x"}, "esc cancel run")
}

// DefaultRespondBinding contributes the standard interaction-response binding.
func DefaultRespondBinding() (agenttui.KeyBinding, error) {
	return standardBinding(agenttui.ActionRespond, []string{"alt+enter"}, "alt+enter respond")
}

// DefaultQuitBinding contributes the standard shell shutdown binding.
func DefaultQuitBinding() (agenttui.KeyBinding, error) {
	return standardBinding(agenttui.ActionQuit, []string{"ctrl+c", "ctrl+q"}, "ctrl+c quit")
}

// DefaultCursorLeftBinding contributes the standard cursor-left binding.
func DefaultCursorLeftBinding() (agenttui.KeyBinding, error) {
	return standardBinding(agenttui.ActionCursorLeft, []string{"left"}, "← move")
}

// DefaultCursorRightBinding contributes the standard cursor-right binding.
func DefaultCursorRightBinding() (agenttui.KeyBinding, error) {
	return standardBinding(agenttui.ActionCursorRight, []string{"right"}, "→ move")
}

// DefaultCursorStartBinding contributes the standard prompt-start binding.
func DefaultCursorStartBinding() (agenttui.KeyBinding, error) {
	return standardBinding(agenttui.ActionCursorStart, []string{"home", "ctrl+a"}, "home start")
}

// DefaultCursorEndBinding contributes the standard prompt-end binding.
func DefaultCursorEndBinding() (agenttui.KeyBinding, error) {
	return standardBinding(agenttui.ActionCursorEnd, []string{"end", "ctrl+e"}, "end finish")
}

// DefaultHistoryPreviousBinding contributes the previous-history binding.
func DefaultHistoryPreviousBinding() (agenttui.KeyBinding, error) {
	return standardBinding(agenttui.ActionHistoryPrevious, []string{"up"}, "↑ history")
}

// DefaultHistoryNextBinding contributes the next-history binding.
func DefaultHistoryNextBinding() (agenttui.KeyBinding, error) {
	return standardBinding(agenttui.ActionHistoryNext, []string{"down"}, "↓ history")
}

// DefaultBackspaceBinding contributes the standard deletion binding.
func DefaultBackspaceBinding() (agenttui.KeyBinding, error) {
	return standardBinding(agenttui.ActionBackspace, []string{"backspace"}, "backspace delete")
}

// DefaultConnectingView constructs a non-revisioned initial view. It does not
// consume revision one from the injected Session's authoritative stream.
func DefaultConnectingView() (agenttui.ViewData, error) {
	title, err := agenttui.NewText("Spice Agent")
	if err != nil {
		return agenttui.ViewData{}, err
	}
	workspace, err := agenttui.NewWorkspace(title, nil)
	if err != nil {
		return agenttui.ViewData{}, err
	}
	message, err := agenttui.NewText("connecting to agent session")
	if err != nil {
		return agenttui.ViewData{}, err
	}
	status, err := agenttui.NewStatus(agenttui.StatusReconnecting, message, nil)
	if err != nil {
		return agenttui.ViewData{}, err
	}
	prompt, err := agenttui.NewEditor("")
	if err != nil {
		return agenttui.ViewData{}, err
	}
	return agenttui.NewViewData(workspace, status, prompt, nil)
}

// DefaultOSTerminalIO binds the current process terminal streams. It opens no
// files and performs no daemon or terminal discovery.
func DefaultOSTerminalIO() (agenttui.TerminalIO, error) {
	return agenttui.NewTerminalIO(os.Stdin, os.Stdout)
}

// DefaultTerminalConfig selects normal styled presentation. Applications may
// replace it with an accessibility-mode value.
func DefaultTerminalConfig() agenttui.TerminalConfig {
	return agenttui.NewTerminalConfig(false)
}

// DefaultShell composes the terminal only after the application supplies an
// exact Session bean. Without that bean Spice leaves this default inactive.
func DefaultShell(
	session agenttui.Session,
	renderer agenttui.Renderer,
	theme agenttui.Theme,
	bindings []agenttui.KeyBinding,
	initial agenttui.ViewData,
	streams agenttui.TerminalIO,
	config agenttui.TerminalConfig,
) (agenttui.Shell, error) {
	return terminal.NewShell(session, renderer, theme, bindings, initial, streams, config)
}

// SpiceAutoConfiguration is statically decoded by Spice and never executed
// during analysis. Every bean is a replaceable fallback selected only by an
// explicit blank import of this package.
func SpiceAutoConfiguration() starter.AutoConfiguration {
	return starter.AutoConfiguration{
		Review: "docs/dependency-review.md",
		Beans: []starter.AutoBean{
			{Factory: DefaultFixedRenderer, Name: "fixedRenderer", Fallback: true},
			{Factory: DefaultDarkTheme, Name: "darkTheme", Fallback: true},
			{Factory: DefaultSubmitBinding, Name: "submitKeyBinding", Fallback: true, Order: 0},
			{Factory: DefaultCancelBinding, Name: "cancelKeyBinding", Fallback: true, Order: 1},
			{Factory: DefaultRespondBinding, Name: "respondKeyBinding", Fallback: true, Order: 2},
			{Factory: DefaultQuitBinding, Name: "quitKeyBinding", Fallback: true, Order: 3},
			{Factory: DefaultCursorLeftBinding, Name: "cursorLeftKeyBinding", Fallback: true, Order: 4},
			{Factory: DefaultCursorRightBinding, Name: "cursorRightKeyBinding", Fallback: true, Order: 5},
			{Factory: DefaultCursorStartBinding, Name: "cursorStartKeyBinding", Fallback: true, Order: 6},
			{Factory: DefaultCursorEndBinding, Name: "cursorEndKeyBinding", Fallback: true, Order: 7},
			{Factory: DefaultHistoryPreviousBinding, Name: "historyPreviousKeyBinding", Fallback: true, Order: 8},
			{Factory: DefaultHistoryNextBinding, Name: "historyNextKeyBinding", Fallback: true, Order: 9},
			{Factory: DefaultBackspaceBinding, Name: "backspaceKeyBinding", Fallback: true, Order: 10},
			{Factory: DefaultConnectingView, Name: "connectingView", Fallback: true},
			{Factory: DefaultOSTerminalIO, Name: "osTerminalIO", Fallback: true},
			{Factory: DefaultTerminalConfig, Name: "terminalConfig", Fallback: true},
			{Factory: DefaultShell, Name: "terminalShell", Fallback: true},
		},
	}
}

func standardBinding(action agenttui.Action, strokes []string, helpValue string) (agenttui.KeyBinding, error) {
	keys := make([]agenttui.Key, 0, len(strokes))
	for _, stroke := range strokes {
		key, err := agenttui.NewKey(stroke, "")
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	help, err := agenttui.NewText(helpValue)
	if err != nil {
		return nil, err
	}
	binding, err := agenttui.NewBinding(action, keys, help)
	if err != nil {
		return nil, err
	}
	return binding, nil
}
