package presentation

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	agenttui "github.com/spice-framework/spice-agent-tui"
)

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
)

// FixedRenderer deterministically fills every terminal cell in a bounded
// semantic frame. It performs no I/O and retains no render state.
type FixedRenderer struct{}

// Render implements agenttui.Renderer.
func (FixedRenderer) Render(data agenttui.ViewData, size agenttui.Size, theme agenttui.Theme) (agenttui.Frame, error) {
	if theme == nil {
		return agenttui.Frame{}, errors.New("render theme must not be nil")
	}
	if err := size.Validate(); err != nil {
		return agenttui.Frame{}, fmt.Errorf("render size: %w", err)
	}
	lines := make([]string, size.Height())
	palette := theme.Palette()
	promptX := 0
	promptY := 0
	promptVisible := false
	if size.Height() > 0 {
		lines[0] = styled(palette.Accent(), true, data.Workspace().Title().String())
	}
	bodyEnd := size.Height()
	if size.Height() >= 2 {
		bodyEnd--
		lines[bodyEnd] = renderStatus(data.Status(), palette)
	}
	if size.Height() >= 3 {
		bodyEnd--
		lines[bodyEnd], promptX = renderPrompt(data.Prompt(), palette, size.Width())
		promptY = bodyEnd
		promptVisible = true
	}
	body := semanticBody(data, palette)
	for index := 1; index < bodyEnd && index-1 < len(body); index++ {
		lines[index] = body[index-1]
	}
	for index := range lines {
		lines[index] = fitLine(lines[index], size.Width())
	}
	frame, err := agenttui.NewFrame(strings.Join(lines, "\n"), size)
	if err != nil || !promptVisible {
		return frame, err
	}
	return frame.WithCursor(promptX, promptY)
}

func semanticBody(data agenttui.ViewData, palette agenttui.Palette) []string {
	result := make([]string, 0)
	for _, section := range data.Workspace().Sections() {
		bodyLines := splitSemanticLines(section.Body().String())
		if len(bodyLines) == 0 {
			bodyLines = []string{""}
		}
		result = append(result, styled(palette.Foreground(), true, section.Title().String()+": ")+
			styled(palette.Foreground(), false, bodyLines[0]))
		for _, line := range bodyLines[1:] {
			result = append(result, styled(palette.Foreground(), false, "  "+line))
		}
	}
	if activity := data.Activity(); len(activity) > 0 {
		result = append(result, styled(palette.Muted(), true, "Activity"))
		for _, item := range activity {
			for _, line := range splitSemanticLines(item.String()) {
				result = append(result, styled(palette.Foreground(), false, "• "+line))
			}
		}
	}
	return result
}

// renderAccessible emits a stable semantic transcript rather than a padded
// terminal canvas. It contains no styling, cursor control, or size-dependent
// whitespace, so a resize does not replay an otherwise unchanged canvas.
func renderAccessible(data agenttui.ViewData) string {
	lines := []string{data.Workspace().Title().String()}
	for _, section := range data.Workspace().Sections() {
		lines = append(lines, section.Title().String()+":")
		for _, line := range splitSemanticLines(section.Body().String()) {
			lines = append(lines, "  "+line)
		}
	}
	if activity := data.Activity(); len(activity) > 0 {
		lines = append(lines, "Activity:")
		for _, item := range activity {
			for _, line := range splitSemanticLines(item.String()) {
				lines = append(lines, "- "+line)
			}
		}
	}
	lines = append(lines, "Prompt: "+data.Prompt().Value().String())
	status := data.Status()
	lines = append(lines, "["+strings.ToUpper(string(status.Level()))+"] "+status.Message().String())
	if hints := status.Hints(); len(hints) > 0 {
		values := make([]string, len(hints))
		for index, hint := range hints {
			values[index] = hint.String()
		}
		lines = append(lines, "Keys: "+strings.Join(values, "; "))
	}
	return strings.Join(lines, "\n")
}

func splitSemanticLines(value string) []string {
	value = strings.ReplaceAll(value, "\t", "    ")
	return strings.Split(value, "\n")
}

func renderPrompt(editor agenttui.EditorState, palette agenttui.Palette, width int) (string, int) {
	const label = "> "
	value := []rune(editor.Value().String())
	cursor := editor.Cursor()
	prefix := string(value[:cursor])
	suffix := string(value[cursor:])
	labelWidth := ansi.StringWidth(label)
	available := max(width-labelWidth, 0)
	if available == 0 {
		return styled(palette.Accent(), true, label), max(width-1, 0)
	}
	prefixWidth := ansi.StringWidth(prefix)
	visiblePrefix := prefix
	if prefixWidth >= available {
		keep := max(available-1, 0)
		visiblePrefix = ansi.TruncateLeft(prefix, prefixWidth-keep, "")
	}
	content := ansi.Truncate(visiblePrefix+suffix, available, "")
	cursorX := min(labelWidth+ansi.StringWidth(visiblePrefix), width-1)
	return styled(palette.Accent(), true, label) + styled(palette.Foreground(), false, content), cursorX
}

func renderStatus(status agenttui.StatusState, palette agenttui.Palette) string {
	color := palette.Muted()
	switch status.Level() {
	case agenttui.StatusBusy, agenttui.StatusReconnecting:
		color = palette.Accent()
	case agenttui.StatusDisconnected, agenttui.StatusWarning:
		color = palette.Warning()
	case agenttui.StatusError:
		color = palette.Failure()
	}
	result := styled(color, true, "["+strings.ToUpper(string(status.Level()))+"] "+status.Message().String())
	if hints := status.Hints(); len(hints) > 0 {
		values := make([]string, len(hints))
		for index, hint := range hints {
			values[index] = hint.String()
		}
		result += styled(palette.Muted(), false, "  "+strings.Join(values, " · "))
	}
	return result
}

func styled(color agenttui.Color, bold bool, value string) string {
	red, green, blue := color.RGB()
	prefix := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", red, green, blue)
	if bold {
		prefix += ansiBold
	}
	return prefix + value + ansiReset
}

func fitLine(value string, width int) string {
	value = ansi.Truncate(value, width, "")
	return value + strings.Repeat(" ", max(width-ansi.StringWidth(value), 0))
}

var _ agenttui.Renderer = FixedRenderer{}
