package agenttui

import "context"

// Shell owns one terminal-program lifecycle. Implementations must honor
// cancellation and must not acquire a daemon or transport implicitly.
type Shell interface {
	Run(context.Context) error
}

// Renderer converts bounded semantic data into one fixed-size terminal frame.
type Renderer interface {
	Render(ViewData, Size, Theme) (Frame, error)
}

// Command is an explicit, injected UI command. This repository does not define
// daemon or coding-agent commands until their owning client contract is adopted.
type Command interface {
	ID() string
	Summary() Text
	Execute(context.Context, Invocation) (CommandResult, error)
}

// PromptEditor is an immutable prompt editing contract.
type PromptEditor interface {
	Value() Text
	Cursor() int
	Insert(string) (EditorState, error)
	Move(CursorMove) EditorState
	Backspace() EditorState
	Clear() EditorState
}

// KeyBinding maps one or more UI-neutral keystrokes to a semantic action.
type KeyBinding interface {
	Action() Action
	Keys() []Key
	Help() Text
	Matches(Key) bool
}

// Workspace exposes an immutable snapshot of named semantic sections.
type Workspace interface {
	Title() Text
	Sections() []Section
}

// StatusBar exposes one immutable status and keyboard-help snapshot.
type StatusBar interface {
	Level() StatusLevel
	Message() Text
	Hints() []Text
}

// Theme exposes an immutable terminal palette without binding callers to a
// rendering library.
type Theme interface {
	Name() string
	Mode() ThemeMode
	Palette() Palette
}
