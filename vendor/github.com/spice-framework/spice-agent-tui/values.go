package agenttui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

const (
	// MaximumTextBytes bounds one semantic text value.
	MaximumTextBytes = 8 << 10
	// MaximumPromptBytes bounds an editable prompt.
	MaximumPromptBytes = 4 << 10
	// MaximumActivityItems bounds one rendered activity snapshot.
	MaximumActivityItems = 128
	// MaximumPromptHistoryItems bounds presentation-owned input history.
	MaximumPromptHistoryItems = 64
	// MaximumViewBytes bounds aggregate semantic text before rendering.
	MaximumViewBytes = 64 << 10
	// MaximumFrameBytes bounds a rendered frame including ANSI styling.
	MaximumFrameBytes = 256 << 10
	// MaximumWidth bounds terminal work for one render.
	MaximumWidth = 240
	// MaximumHeight bounds terminal work for one render.
	MaximumHeight   = 100
	maximumSections = 32
	maximumHints    = 16
)

var errInvalidText = errors.New("semantic text must be valid UTF-8 without terminal control characters")

// Text is validated terminal-safe semantic text. It may contain newlines and
// tabs, but never escape sequences or other control characters.
type Text struct{ value string }

// NewText validates and copies one bounded semantic text value.
func NewText(value string) (Text, error) {
	text := Text{value: value}
	return text, text.Validate()
}

// String returns the validated text.
func (text Text) String() string { return text.value }

// Validate reports whether text remains bounded and terminal-safe.
func (text Text) Validate() error {
	if !utf8.ValidString(text.value) || len(text.value) > MaximumTextBytes {
		return errInvalidText
	}
	for _, character := range text.value {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return errInvalidText
		}
	}
	return nil
}

func validateSingleLine(text Text, name string) error {
	if strings.ContainsAny(text.value, "\n\t") {
		return fmt.Errorf("%s must be a single line", name)
	}
	return nil
}

// Size is a bounded terminal cell size.
type Size struct {
	width  int
	height int
}

// NewSize validates a terminal size.
func NewSize(width, height int) (Size, error) {
	size := Size{width: width, height: height}
	return size, size.Validate()
}

// BoundedSize clamps arbitrary dimensions to the supported rendering bounds.
func BoundedSize(width, height int) Size {
	return Size{width: min(max(width, 1), MaximumWidth), height: min(max(height, 1), MaximumHeight)}
}

// Width returns terminal columns.
func (size Size) Width() int { return size.width }

// Height returns terminal rows.
func (size Size) Height() int { return size.height }

// Validate reports whether the dimensions are within rendering bounds.
func (size Size) Validate() error {
	if size.width < 1 || size.width > MaximumWidth || size.height < 1 || size.height > MaximumHeight {
		return fmt.Errorf("terminal size must be within 1x1 and %dx%d", MaximumWidth, MaximumHeight)
	}
	return nil
}

// Section is one immutable workspace region.
type Section struct {
	title Text
	body  Text
}

// NewSection constructs a named workspace region.
func NewSection(title, body Text) (Section, error) {
	section := Section{title: title, body: body}
	return section, section.Validate()
}

// Title returns the section title.
func (section Section) Title() Text { return section.title }

// Body returns the section body.
func (section Section) Body() Text { return section.body }

// Validate reports whether the section is complete and terminal-safe.
func (section Section) Validate() error {
	if err := section.title.Validate(); err != nil {
		return fmt.Errorf("section title: %w", err)
	}
	if err := section.body.Validate(); err != nil {
		return fmt.Errorf("section body: %w", err)
	}
	if section.title.String() == "" {
		return errors.New("section title must not be empty")
	}
	return validateSingleLine(section.title, "section title")
}

// WorkspaceState is an immutable Workspace implementation.
type WorkspaceState struct {
	title    Text
	sections []Section
}

// NewWorkspace constructs a bounded workspace snapshot.
func NewWorkspace(title Text, sections []Section) (WorkspaceState, error) {
	workspace := WorkspaceState{title: title, sections: slices.Clone(sections)}
	return workspace, workspace.Validate()
}

// Title returns the workspace title.
func (workspace WorkspaceState) Title() Text { return workspace.title }

// Sections returns a defensive copy in source order.
func (workspace WorkspaceState) Sections() []Section { return slices.Clone(workspace.sections) }

// Validate reports whether the entire workspace snapshot is well formed.
func (workspace WorkspaceState) Validate() error {
	if err := workspace.title.Validate(); err != nil {
		return fmt.Errorf("workspace title: %w", err)
	}
	if workspace.title.String() == "" {
		return errors.New("workspace title must not be empty")
	}
	if err := validateSingleLine(workspace.title, "workspace title"); err != nil {
		return err
	}
	if len(workspace.sections) > maximumSections {
		return fmt.Errorf("workspace sections exceed %d", maximumSections)
	}
	for index, section := range workspace.sections {
		if err := section.Validate(); err != nil {
			return fmt.Errorf("workspace section %d: %w", index, err)
		}
	}
	return nil
}

// StatusLevel classifies status presentation without prescribing a color.
type StatusLevel string

const (
	// StatusReady indicates normal availability.
	StatusReady StatusLevel = "ready"
	// StatusBusy indicates bounded work in progress.
	StatusBusy StatusLevel = "busy"
	// StatusDisconnected indicates that no session is currently available.
	StatusDisconnected StatusLevel = "disconnected"
	// StatusReconnecting indicates an explicit bounded reconnection attempt.
	StatusReconnecting StatusLevel = "reconnecting"
	// StatusWarning indicates degraded but usable state.
	StatusWarning StatusLevel = "warning"
	// StatusError indicates a visible failure.
	StatusError StatusLevel = "error"
)

func validStatusLevel(level StatusLevel) bool {
	return level == StatusReady || level == StatusBusy || level == StatusDisconnected ||
		level == StatusReconnecting || level == StatusWarning || level == StatusError
}

// StatusState is an immutable StatusBar implementation.
type StatusState struct {
	level   StatusLevel
	message Text
	hints   []Text
}

// NewStatus constructs a bounded status snapshot.
func NewStatus(level StatusLevel, message Text, hints []Text) (StatusState, error) {
	status := StatusState{level: level, message: message, hints: slices.Clone(hints)}
	return status, status.Validate()
}

// Level returns status severity.
func (status StatusState) Level() StatusLevel { return status.level }

// Message returns status text.
func (status StatusState) Message() Text { return status.message }

// Hints returns a defensive copy.
func (status StatusState) Hints() []Text { return slices.Clone(status.hints) }

// Validate reports whether the status snapshot is bounded and complete.
func (status StatusState) Validate() error {
	if !validStatusLevel(status.level) {
		return fmt.Errorf("unsupported status level %q", status.level)
	}
	if err := status.message.Validate(); err != nil {
		return fmt.Errorf("status message: %w", err)
	}
	if err := validateSingleLine(status.message, "status message"); err != nil {
		return err
	}
	if len(status.hints) > maximumHints {
		return fmt.Errorf("status hints exceed %d", maximumHints)
	}
	for index, hint := range status.hints {
		if err := hint.Validate(); err != nil {
			return fmt.Errorf("status hint %d: %w", index, err)
		}
		if err := validateSingleLine(hint, "status hint"); err != nil {
			return err
		}
	}
	return nil
}

// ThemeMode selects a light or dark semantic palette.
type ThemeMode string

const (
	// ThemeLight is optimized for light terminal backgrounds.
	ThemeLight ThemeMode = "light"
	// ThemeDark is optimized for dark terminal backgrounds.
	ThemeDark ThemeMode = "dark"
)

// Color is an immutable RGB color.
type Color struct{ red, green, blue uint8 }

// NewColor constructs an RGB color.
func NewColor(red, green, blue uint8) Color { return Color{red: red, green: green, blue: blue} }

// RGB returns the individual color components.
func (color Color) RGB() (uint8, uint8, uint8) { return color.red, color.green, color.blue }

// Palette is an immutable semantic color collection.
type Palette struct {
	foreground Color
	muted      Color
	accent     Color
	warning    Color
	failure    Color
}

// NewPalette constructs one semantic palette.
func NewPalette(foreground, muted, accent, warning, failure Color) Palette {
	return Palette{foreground: foreground, muted: muted, accent: accent, warning: warning, failure: failure}
}

// Foreground returns the normal text color.
func (palette Palette) Foreground() Color { return palette.foreground }

// Muted returns the secondary text color.
func (palette Palette) Muted() Color { return palette.muted }

// Accent returns the active text color.
func (palette Palette) Accent() Color { return palette.accent }

// Warning returns the warning color.
func (palette Palette) Warning() Color { return palette.warning }

// Failure returns the failure color.
func (palette Palette) Failure() Color { return palette.failure }

// ThemeState is an immutable Theme implementation.
type ThemeState struct {
	name    string
	mode    ThemeMode
	palette Palette
}

// NewTheme constructs a named theme.
func NewTheme(name string, mode ThemeMode, palette Palette) (ThemeState, error) {
	theme := ThemeState{name: name, mode: mode, palette: palette}
	return theme, theme.Validate()
}

// Name returns the stable theme name.
func (theme ThemeState) Name() string { return theme.name }

// Mode returns the background mode.
func (theme ThemeState) Mode() ThemeMode { return theme.mode }

// Palette returns the immutable semantic palette.
func (theme ThemeState) Palette() Palette { return theme.palette }

// Validate reports whether the theme has a stable identity and supported mode.
func (theme ThemeState) Validate() error {
	if theme.name == "" || theme.name != strings.TrimSpace(theme.name) || strings.ContainsAny(theme.name, "\n\t") {
		return errors.New("theme name must be non-empty without surrounding whitespace")
	}
	if theme.mode != ThemeLight && theme.mode != ThemeDark {
		return fmt.Errorf("unsupported theme mode %q", theme.mode)
	}
	return nil
}

// LightTheme returns the built-in deterministic light palette.
func LightTheme() ThemeState {
	return ThemeState{name: "spice-light", mode: ThemeLight, palette: NewPalette(
		NewColor(31, 41, 55), NewColor(100, 116, 139), NewColor(3, 105, 161),
		NewColor(161, 98, 7), NewColor(185, 28, 28),
	)}
}

// DarkTheme returns the built-in deterministic dark palette.
func DarkTheme() ThemeState {
	return ThemeState{name: "spice-dark", mode: ThemeDark, palette: NewPalette(
		NewColor(226, 232, 240), NewColor(148, 163, 184), NewColor(56, 189, 248),
		NewColor(250, 204, 21), NewColor(248, 113, 113),
	)}
}

// Frame is one immutable rendered terminal frame.
type Frame struct {
	content       string
	size          Size
	cursorX       int
	cursorY       int
	cursorVisible bool
}

// NewFrame constructs a bounded rendered frame.
func NewFrame(content string, size Size) (Frame, error) {
	frame := Frame{content: content, size: size}
	return frame, frame.Validate()
}

// Content returns rendered terminal content.
func (frame Frame) Content() string { return frame.content }

// PlainContent returns the same fixed-size frame without terminal styling.
// Status levels remain visible text and never depend on color alone.
func (frame Frame) PlainContent() string { return ansi.Strip(frame.content) }

// Size returns the rendered size.
func (frame Frame) Size() Size { return frame.size }

// WithCursor returns a frame with a visible, cell-addressed terminal cursor.
func (frame Frame) WithCursor(x, y int) (Frame, error) {
	frame.cursorX = x
	frame.cursorY = y
	frame.cursorVisible = true
	return frame, frame.Validate()
}

// Cursor returns the cursor cell and whether it is visible.
func (frame Frame) Cursor() (x, y int, visible bool) {
	return frame.cursorX, frame.cursorY, frame.cursorVisible
}

// Validate reports whether the frame is bounded and has a valid terminal size.
func (frame Frame) Validate() error {
	if err := frame.size.Validate(); err != nil {
		return fmt.Errorf("rendered frame size: %w", err)
	}
	if len(frame.content) > MaximumFrameBytes {
		return fmt.Errorf("rendered frame exceeds %d bytes", MaximumFrameBytes)
	}
	if frame.cursorVisible && (frame.cursorX < 0 || frame.cursorX >= frame.size.Width() ||
		frame.cursorY < 0 || frame.cursorY >= frame.size.Height()) {
		return errors.New("rendered cursor is outside the frame")
	}
	return nil
}

// ViewData is one immutable bounded semantic render snapshot.
type ViewData struct {
	workspace WorkspaceState
	status    StatusState
	prompt    EditorState
	activity  []Text
}

// NewViewData constructs a bounded semantic snapshot.
func NewViewData(
	workspace WorkspaceState,
	status StatusState,
	prompt EditorState,
	activity []Text,
) (ViewData, error) {
	view := ViewData{workspace: workspace, status: status, prompt: prompt, activity: slices.Clone(activity)}
	return view, view.Validate()
}

// Validate reports whether the complete semantic render snapshot is safe and bounded.
func (view ViewData) Validate() error {
	if err := view.workspace.Validate(); err != nil {
		return fmt.Errorf("semantic view workspace: %w", err)
	}
	if err := view.status.Validate(); err != nil {
		return fmt.Errorf("semantic view status: %w", err)
	}
	if err := view.prompt.Validate(); err != nil {
		return fmt.Errorf("semantic view prompt: %w", err)
	}
	if len(view.activity) > MaximumActivityItems {
		return fmt.Errorf("activity items exceed %d", MaximumActivityItems)
	}
	total := len(view.workspace.Title().String()) + len(view.status.Message().String()) + len(view.prompt.Value().String())
	for _, section := range view.workspace.Sections() {
		total += len(section.Title().String()) + len(section.Body().String())
	}
	for _, hint := range view.status.Hints() {
		total += len(hint.String())
	}
	for index, item := range view.activity {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("activity item %d: %w", index, err)
		}
		total += len(item.String())
	}
	if total > MaximumViewBytes {
		return fmt.Errorf("semantic view exceeds %d bytes", MaximumViewBytes)
	}
	return nil
}

// Workspace returns the immutable workspace snapshot.
func (view ViewData) Workspace() WorkspaceState { return view.workspace }

// Status returns the immutable status snapshot.
func (view ViewData) Status() StatusState { return view.status }

// Prompt returns the immutable prompt snapshot.
func (view ViewData) Prompt() EditorState { return view.prompt }

// Activity returns a defensive copy.
func (view ViewData) Activity() []Text { return slices.Clone(view.activity) }

var (
	_ Workspace = WorkspaceState{}
	_ StatusBar = StatusState{}
	_ Theme     = ThemeState{}
)
