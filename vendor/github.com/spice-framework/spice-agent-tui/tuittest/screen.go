package tuittest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	agenttui "github.com/spice-framework/spice-agent-tui"
)

// Screen is one immutable, agent-readable terminal capture.
type Screen struct {
	name          string
	width         int
	height        int
	styled        string
	plain         string
	plainLines    []string
	cursorX       int
	cursorY       int
	cursorVisible bool
	altScreen     bool
	accessible    bool
	prompt        string
	status        string
	statusLevel   string
	activity      []string
	revision      uint64
}

// Name returns an optional scenario label.
func (screen Screen) Name() string { return screen.name }

// Width returns the fixed frame width in display cells.
func (screen Screen) Width() int { return screen.width }

// Height returns the fixed frame height in lines.
func (screen Screen) Height() int { return screen.height }

// Styled returns the normalized styled frame used for pixel-perfect goldens.
// ESC bytes are rendered as the token <ESC> for stable, reviewable files.
func (screen Screen) Styled() string { return screen.styled }

// Plain returns the fixed-size frame with ANSI styling stripped.
func (screen Screen) Plain() string { return screen.plain }

// Lines returns an exact defensive copy of the rendered plain lines.
func (screen Screen) Lines() []string { return append([]string(nil), screen.plainLines...) }

// Cursor returns the rendered caret position when visible.
func (screen Screen) Cursor() (x, y int, visible bool) {
	return screen.cursorX, screen.cursorY, screen.cursorVisible
}

// AlternateScreen reports whether a virtual-terminal capture is using the
// alternate screen buffer or a semantic model requests alternate-screen mode.
func (screen Screen) AlternateScreen() bool { return screen.altScreen }

// Prompt returns the current editor value.
func (screen Screen) Prompt() string { return screen.prompt }

// Status returns the current status message text.
func (screen Screen) Status() string { return screen.status }

// StatusLevel returns the current status level string.
func (screen Screen) StatusLevel() string { return screen.statusLevel }

// Activity returns activity item strings in display order.
func (screen Screen) Activity() []string { return append([]string(nil), screen.activity...) }

// Revision returns the latest accepted session revision.
func (screen Screen) Revision() uint64 { return screen.revision }

// Accessible reports whether the screen was captured in accessible mode.
func (screen Screen) Accessible() bool { return screen.accessible }

// Digest returns a SHA-256 digest of the complete observable screen state.
// The digest includes rendered cells, cursor and alternate-screen state, and
// all semantic metadata. It is stable across operating systems and runs.
func (screen Screen) Digest() string {
	sum := sha256.Sum256(screen.canonicalState())
	return hex.EncodeToString(sum[:])
}

// Contains reports whether plain content contains value.
func (screen Screen) Contains(value string) bool {
	return strings.Contains(screen.plain, value)
}

// Line returns one plain line by zero-based index.
func (screen Screen) Line(index int) (string, error) {
	if index < 0 || index >= len(screen.plainLines) {
		return "", fmt.Errorf("line %d out of range [0,%d)", index, len(screen.plainLines))
	}
	return screen.plainLines[index], nil
}

// EqualStyled reports exact pixel-perfect styled equality after normalization.
func (screen Screen) EqualStyled(other Screen) bool {
	return screen.styled == other.styled &&
		screen.width == other.width &&
		screen.height == other.height &&
		screen.cursorX == other.cursorX &&
		screen.cursorY == other.cursorY &&
		screen.cursorVisible == other.cursorVisible &&
		screen.altScreen == other.altScreen
}

// EqualPlain reports exact plain-frame equality including dimensions.
func (screen Screen) EqualPlain(other Screen) bool {
	return screen.plain == other.plain &&
		screen.width == other.width &&
		screen.height == other.height
}

// Diff returns a human/agent-readable mismatch description.
func (screen Screen) Diff(other Screen) string {
	if screen.EqualStyled(other) && screen.EqualPlain(other) {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("screen mismatch\n")
	writeMetaDiff(&builder, "width", screen.width, other.width)
	writeMetaDiff(&builder, "height", screen.height, other.height)
	writeMetaDiff(&builder, "cursorX", screen.cursorX, other.cursorX)
	writeMetaDiff(&builder, "cursorY", screen.cursorY, other.cursorY)
	writeMetaDiff(&builder, "cursorVisible", screen.cursorVisible, other.cursorVisible)
	writeMetaDiff(&builder, "alternateScreen", screen.altScreen, other.altScreen)
	if screen.styled != other.styled {
		builder.WriteString("--- got styled ---\n")
		builder.WriteString(screen.styled)
		builder.WriteString("\n--- want styled ---\n")
		builder.WriteString(other.styled)
		builder.WriteByte('\n')
	}
	if screen.plain != other.plain {
		builder.WriteString("--- got plain ---\n")
		builder.WriteString(screen.plain)
		builder.WriteString("\n--- want plain ---\n")
		builder.WriteString(other.plain)
		builder.WriteByte('\n')
	}
	return builder.String()
}

// String returns the plain frame for quick fmt/log use.
func (screen Screen) String() string { return screen.plain }

// AgentReport returns a multi-section dump optimized for coding-agent logs.
func (screen Screen) AgentReport() string {
	var builder strings.Builder
	if screen.name != "" {
		builder.WriteString("name: ")
		builder.WriteString(screen.name)
		builder.WriteByte('\n')
	}
	fmt.Fprintf(&builder, "size: %dx%d\n", screen.width, screen.height)
	fmt.Fprintf(&builder, "cursor: x=%d y=%d visible=%t\n", screen.cursorX, screen.cursorY, screen.cursorVisible)
	fmt.Fprintf(&builder, "alt_screen: %t accessible: %t revision: %d\n", screen.altScreen, screen.accessible, screen.revision)
	fmt.Fprintf(&builder, "status: [%s] %s\n", strings.ToUpper(screen.statusLevel), screen.status)
	fmt.Fprintf(&builder, "prompt: %q\n", screen.prompt)
	if len(screen.activity) > 0 {
		builder.WriteString("activity:\n")
		for _, item := range screen.activity {
			builder.WriteString("  - ")
			builder.WriteString(item)
			builder.WriteByte('\n')
		}
	}
	builder.WriteString("plain:\n")
	builder.WriteString(screen.plain)
	if !strings.HasSuffix(screen.plain, "\n") {
		builder.WriteByte('\n')
	}
	builder.WriteString("styled:\n")
	builder.WriteString(screen.styled)
	if !strings.HasSuffix(screen.styled, "\n") {
		builder.WriteByte('\n')
	}
	return builder.String()
}

// FromFrame constructs a Screen from a rendered Frame.
func FromFrame(frame agenttui.Frame, opts ...ScreenOption) (Screen, error) {
	if err := frame.Validate(); err != nil {
		return Screen{}, err
	}
	screen := Screen{
		width:  frame.Size().Width(),
		height: frame.Size().Height(),
		styled: NormalizeStyled(frame.Content()),
		plain:  frame.PlainContent(),
	}
	screen.cursorX, screen.cursorY, screen.cursorVisible = frame.Cursor()
	screen.plainLines = splitLines(screen.plain)
	for _, opt := range opts {
		opt(&screen)
	}
	return screen, nil
}

// ScreenOption mutates optional Screen metadata.
type ScreenOption func(*Screen)

// WithName labels a screen for scenario dumps and golden basenames.
func WithName(name string) ScreenOption {
	return func(screen *Screen) { screen.name = name }
}

// WithSemantic attaches model-owned semantic fields for agent inspection.
func WithSemantic(
	prompt string,
	statusLevel, status string,
	activity []string,
	revision uint64,
	accessible bool,
	altScreen bool,
) ScreenOption {
	return func(screen *Screen) {
		screen.prompt = prompt
		screen.statusLevel = statusLevel
		screen.status = status
		screen.activity = append([]string(nil), activity...)
		screen.revision = revision
		screen.accessible = accessible
		screen.altScreen = altScreen
	}
}

// NormalizeStyled rewrites styled frame content for stable golden comparison.
// It matches the existing presentation golden convention.
func NormalizeStyled(content string) string {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		// Preserve exact display width semantics by only converting ESC and
		// trimming purely decorative trailing spaces introduced by fixed fill.
		lines[index] = strings.ReplaceAll(strings.TrimRight(line, " "), "\x1b", "<ESC>")
	}
	return strings.Join(lines, "\n")
}

// DenormalizeStyled converts golden <ESC> tokens back into ANSI ESC bytes.
func DenormalizeStyled(content string) string {
	return strings.ReplaceAll(content, "<ESC>", "\x1b")
}

func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

func writeMetaDiff[T comparable](builder *strings.Builder, name string, got, want T) {
	if got != want {
		fmt.Fprintf(builder, "%s: got %#v want %#v\n", name, got, want)
	}
}

func (screen Screen) canonicalState() []byte {
	state := struct {
		Name          string   `json:"name"`
		Width         int      `json:"width"`
		Height        int      `json:"height"`
		Styled        string   `json:"styled"`
		Plain         string   `json:"plain"`
		PlainLines    []string `json:"plain_lines"`
		CursorX       int      `json:"cursor_x"`
		CursorY       int      `json:"cursor_y"`
		CursorVisible bool     `json:"cursor_visible"`
		AltScreen     bool     `json:"alternate_screen"`
		Accessible    bool     `json:"accessible"`
		Prompt        string   `json:"prompt"`
		Status        string   `json:"status"`
		StatusLevel   string   `json:"status_level"`
		Activity      []string `json:"activity"`
		Revision      uint64   `json:"revision"`
	}{
		Name:          screen.name,
		Width:         screen.width,
		Height:        screen.height,
		Styled:        screen.styled,
		Plain:         screen.plain,
		PlainLines:    append([]string(nil), screen.plainLines...),
		CursorX:       screen.cursorX,
		CursorY:       screen.cursorY,
		CursorVisible: screen.cursorVisible,
		AltScreen:     screen.altScreen,
		Accessible:    screen.accessible,
		Prompt:        screen.prompt,
		Status:        screen.status,
		StatusLevel:   screen.statusLevel,
		Activity:      append([]string(nil), screen.activity...),
		Revision:      screen.revision,
	}
	if state.Activity == nil {
		state.Activity = []string{}
	}
	if state.PlainLines == nil {
		state.PlainLines = []string{}
	}
	content, err := json.Marshal(state)
	if err != nil {
		panic("marshal immutable screen state: " + err.Error())
	}
	return content
}

// CellWidth returns the ANSI display width of one line.
func CellWidth(line string) int { return ansi.StringWidth(line) }
