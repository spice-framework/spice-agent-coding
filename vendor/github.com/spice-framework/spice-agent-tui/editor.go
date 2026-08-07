package agenttui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// CursorMove is a relative editor movement.
type CursorMove int

const (
	// MoveLeft moves one user-perceived character toward the start.
	MoveLeft CursorMove = -1
	// MoveRight moves one user-perceived character toward the end.
	MoveRight CursorMove = 1
	// MoveStart moves to the start of the prompt.
	MoveStart CursorMove = -2
	// MoveEnd moves to the end of the prompt.
	MoveEnd CursorMove = 2
)

// EditorState is an immutable PromptEditor implementation.
type EditorState struct {
	value  string
	cursor int
}

// NewEditor constructs an editor with its cursor after the initial value.
func NewEditor(initial string) (EditorState, error) {
	editor := EditorState{value: initial, cursor: utf8.RuneCountInString(initial)}
	return editor, editor.Validate()
}

// Value returns prompt text.
func (editor EditorState) Value() Text { return Text{value: editor.value} }

// Cursor returns the zero-based rune offset at a grapheme boundary.
func (editor EditorState) Cursor() int { return editor.cursor }

// Validate reports whether prompt text and its rune cursor are well formed.
func (editor EditorState) Validate() error {
	if err := validatePromptText(editor.value); err != nil {
		return err
	}
	if !slices.Contains(graphemeBoundaries(editor.value), editor.cursor) {
		return errors.New("prompt cursor is outside its text")
	}
	return nil
}

// Insert returns a new editor with text inserted at the cursor.
func (editor EditorState) Insert(value string) (EditorState, error) {
	if err := editor.Validate(); err != nil {
		return EditorState{}, fmt.Errorf("edit invalid prompt: %w", err)
	}
	if err := validatePromptText(value); err != nil {
		return EditorState{}, err
	}
	current := []rune(editor.value)
	inserted := []rune(value)
	combined := make([]rune, 0, len(current)+len(inserted))
	combined = append(combined, current[:editor.cursor]...)
	combined = append(combined, inserted...)
	combined = append(combined, current[editor.cursor:]...)
	result := string(combined)
	if len(result) > MaximumPromptBytes {
		return EditorState{}, fmt.Errorf("prompt exceeds %d bytes", MaximumPromptBytes)
	}
	cursor := nextGraphemeBoundary(result, editor.cursor+len(inserted))
	return EditorState{value: result, cursor: cursor}, nil
}

// Move returns a new editor at the adjacent user-perceived character boundary.
func (editor EditorState) Move(direction CursorMove) EditorState {
	if editor.Validate() != nil {
		return EditorState{}
	}
	boundaries := graphemeBoundaries(editor.value)
	index := slices.Index(boundaries, editor.cursor)
	switch direction {
	case MoveLeft:
		editor.cursor = boundaries[max(index-1, 0)]
	case MoveRight:
		editor.cursor = boundaries[min(index+1, len(boundaries)-1)]
	case MoveStart:
		editor.cursor = 0
	case MoveEnd:
		editor.cursor = boundaries[len(boundaries)-1]
	}
	return editor
}

// Backspace removes the rune immediately before the cursor.
func (editor EditorState) Backspace() EditorState {
	if editor.Validate() != nil {
		return EditorState{}
	}
	if editor.cursor == 0 {
		return editor
	}
	boundaries := graphemeBoundaries(editor.value)
	index := slices.Index(boundaries, editor.cursor)
	start := boundaries[index-1]
	current := []rune(editor.value)
	current = slices.Delete(current, start, editor.cursor)
	return EditorState{value: string(current), cursor: start}
}

// Clear returns an empty editor.
func (EditorState) Clear() EditorState { return EditorState{} }

func validatePromptText(value string) error {
	if !utf8.ValidString(value) || len(value) > MaximumPromptBytes {
		return errors.New("prompt must be bounded valid UTF-8")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("prompt must not contain control characters")
		}
	}
	return nil
}

func graphemeBoundaries(value string) []int {
	boundaries := []int{0}
	runeOffset := 0
	graphemes := uniseg.NewGraphemes(value)
	for graphemes.Next() {
		runeOffset += utf8.RuneCountInString(graphemes.Str())
		boundaries = append(boundaries, runeOffset)
	}
	return boundaries
}

func nextGraphemeBoundary(value string, desired int) int {
	for _, boundary := range graphemeBoundaries(value) {
		if boundary >= desired {
			return boundary
		}
	}
	return utf8.RuneCountInString(value)
}

// Action identifies one presentation-only keyboard operation.
type Action string

const (
	// ActionQuit explicitly requests normal shell termination.
	ActionQuit Action = "quit"
	// ActionSubmit requests submission of the current prompt.
	ActionSubmit Action = "submit"
	// ActionCancelActiveRun requests cancellation of the current run without
	// terminating the terminal application.
	ActionCancelActiveRun Action = "cancel-active-run"
	// ActionRespond submits the current prompt to a pending interaction.
	ActionRespond Action = "respond"
	// ActionCursorLeft moves the prompt cursor left.
	ActionCursorLeft Action = "cursor-left"
	// ActionCursorRight moves the prompt cursor right.
	ActionCursorRight Action = "cursor-right"
	// ActionCursorStart moves the prompt cursor to the start.
	ActionCursorStart Action = "cursor-start"
	// ActionCursorEnd moves the prompt cursor to the end.
	ActionCursorEnd Action = "cursor-end"
	// ActionHistoryPrevious selects the previous bounded prompt-history entry.
	ActionHistoryPrevious Action = "history-previous"
	// ActionHistoryNext selects the next bounded prompt-history entry.
	ActionHistoryNext Action = "history-next"
	// ActionBackspace removes the previous user-perceived character.
	ActionBackspace Action = "backspace"
)

func validAction(action Action) bool {
	return action == ActionQuit || action == ActionSubmit || action == ActionCancelActiveRun ||
		action == ActionRespond || action == ActionCursorLeft || action == ActionCursorRight ||
		action == ActionCursorStart || action == ActionCursorEnd || action == ActionHistoryPrevious ||
		action == ActionHistoryNext || action == ActionBackspace
}

// Key is one immutable UI-neutral key event.
type Key struct {
	stroke string
	text   string
}

// NewKey constructs a canonical key event.
func NewKey(stroke, text string) (Key, error) {
	stroke = strings.ToLower(strings.TrimSpace(stroke))
	key := Key{stroke: stroke, text: text}
	return key, key.Validate()
}

// Stroke returns the canonical physical key identity.
func (key Key) Stroke() string { return key.stroke }

// Text returns printable inserted text, if any.
func (key Key) Text() string { return key.text }

// Validate reports whether the key identity and printable text are safe.
func (key Key) Validate() error {
	if key.stroke == "" || len(key.stroke) > 64 || strings.ContainsAny(key.stroke, "\n\t\x1b") {
		return errors.New("keystroke must be a bounded single-line identity")
	}
	return validatePromptText(key.text)
}

// Binding is an immutable KeyBinding implementation.
type Binding struct {
	action Action
	keys   []Key
	help   Text
}

// StandardKeyBindings constructs the default ordered bindings for a terminal
// application composition root. The presentation model never selects these
// implicitly; applications may inject a different complete set.
func StandardKeyBindings() ([]KeyBinding, error) {
	specifications := []struct {
		action Action
		keys   []string
		help   string
	}{
		{action: ActionSubmit, keys: []string{"enter"}, help: "enter submit"},
		{action: ActionCancelActiveRun, keys: []string{"esc", "ctrl+x"}, help: "esc cancel run"},
		{action: ActionRespond, keys: []string{"alt+enter"}, help: "alt+enter respond"},
		{action: ActionQuit, keys: []string{"ctrl+c", "ctrl+q"}, help: "ctrl+c quit"},
		{action: ActionCursorLeft, keys: []string{"left"}, help: "← move"},
		{action: ActionCursorRight, keys: []string{"right"}, help: "→ move"},
		{action: ActionCursorStart, keys: []string{"home", "ctrl+a"}, help: "home start"},
		{action: ActionCursorEnd, keys: []string{"end", "ctrl+e"}, help: "end finish"},
		{action: ActionHistoryPrevious, keys: []string{"up"}, help: "↑ history"},
		{action: ActionHistoryNext, keys: []string{"down"}, help: "↓ history"},
		{action: ActionBackspace, keys: []string{"backspace"}, help: "backspace delete"},
	}
	result := make([]KeyBinding, 0, len(specifications))
	for _, specification := range specifications {
		keys := make([]Key, 0, len(specification.keys))
		for _, value := range specification.keys {
			key, err := NewKey(value, "")
			if err != nil {
				return nil, err
			}
			keys = append(keys, key)
		}
		help, err := NewText(specification.help)
		if err != nil {
			return nil, err
		}
		binding, err := NewBinding(specification.action, keys, help)
		if err != nil {
			return nil, err
		}
		result = append(result, binding)
	}
	return result, nil
}

// NewBinding constructs a semantic key binding.
func NewBinding(action Action, keys []Key, help Text) (Binding, error) {
	binding := Binding{action: action, keys: slices.Clone(keys), help: help}
	return binding, binding.Validate()
}

// Action returns the semantic action.
func (binding Binding) Action() Action { return binding.action }

// Keys returns a defensive copy.
func (binding Binding) Keys() []Key { return slices.Clone(binding.keys) }

// Help returns accessible key help.
func (binding Binding) Help() Text { return binding.help }

// Matches reports whether the canonical keystroke matches.
func (binding Binding) Matches(key Key) bool {
	if binding.Validate() != nil || key.Validate() != nil {
		return false
	}
	return slices.ContainsFunc(binding.keys, func(candidate Key) bool { return candidate.stroke == key.stroke })
}

// Validate reports whether the binding contains complete safe key metadata.
func (binding Binding) Validate() error {
	if !validAction(binding.action) {
		return fmt.Errorf("unsupported key action %q", binding.action)
	}
	if len(binding.keys) == 0 || len(binding.keys) > 8 {
		return errors.New("key binding must contain between one and eight keys")
	}
	for index, key := range binding.keys {
		if err := key.Validate(); err != nil {
			return fmt.Errorf("key binding key %d: %w", index, err)
		}
	}
	if err := binding.help.Validate(); err != nil {
		return fmt.Errorf("key help: %w", err)
	}
	return validateSingleLine(binding.help, "key help")
}

var (
	_ PromptEditor = EditorState{}
	_ KeyBinding   = Binding{}
)
