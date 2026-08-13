package tuittest

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// keyPress builds a Bubble Tea key message whose Keystroke() matches the TUI
// binding identities (enter, esc, ctrl+c, left, ...).
func keyPress(stroke, text string) (tea.KeyPressMsg, error) {
	stroke = strings.ToLower(strings.TrimSpace(stroke))
	if stroke == "" {
		return tea.KeyPressMsg{}, fmt.Errorf("keystroke must not be empty")
	}
	key, err := teaKey(stroke, text)
	if err != nil {
		return tea.KeyPressMsg{}, err
	}
	return tea.KeyPressMsg(key), nil
}

func teaKey(stroke, text string) (tea.Key, error) {
	if key, ok := basicKey(stroke); ok {
		return key, nil
	}
	if stroke == "runes" || stroke == "text" {
		if text == "" {
			return tea.Key{}, fmt.Errorf("text keystroke requires printable text")
		}
		return textKey(text)
	}
	if strings.HasPrefix(stroke, "ctrl+") {
		return modifiedKey(stroke, "ctrl+", tea.ModCtrl)
	}
	if strings.HasPrefix(stroke, "alt+") {
		return modifiedKey(stroke, "alt+", tea.ModAlt)
	}
	if text == "" {
		text = stroke
	}
	return textKey(text)
}

func basicKey(stroke string) (tea.Key, bool) {
	switch stroke {
	case "enter", "return":
		return tea.Key{Code: tea.KeyEnter}, true
	case "esc", "escape":
		return tea.Key{Code: tea.KeyEscape}, true
	case "backspace":
		return tea.Key{Code: tea.KeyBackspace}, true
	case "left":
		return tea.Key{Code: tea.KeyLeft}, true
	case "right":
		return tea.Key{Code: tea.KeyRight}, true
	case "up":
		return tea.Key{Code: tea.KeyUp}, true
	case "down":
		return tea.Key{Code: tea.KeyDown}, true
	case "home":
		return tea.Key{Code: tea.KeyHome}, true
	case "end":
		return tea.Key{Code: tea.KeyEnd}, true
	case "tab":
		return tea.Key{Code: tea.KeyTab}, true
	case "space":
		return tea.Key{Code: ' ', Text: " "}, true
	case "alt+enter":
		return tea.Key{Code: tea.KeyEnter, Mod: tea.ModAlt}, true
	default:
		return tea.Key{}, false
	}
}

func modifiedKey(stroke, prefix string, modifier tea.KeyMod) (tea.Key, error) {
	value := strings.TrimPrefix(stroke, prefix)
	if utf8.RuneCountInString(value) != 1 {
		return tea.Key{}, fmt.Errorf("unsupported modified keystroke %q", stroke)
	}
	r, _ := utf8.DecodeRuneInString(value)
	if r == utf8.RuneError || r < 0x20 || r == 0x7f {
		return tea.Key{}, fmt.Errorf("unsupported modified keystroke %q", stroke)
	}
	return tea.Key{Code: r, Mod: modifier}, nil
}

func textKey(text string) (tea.Key, error) {
	if text == "" || strings.ContainsAny(text, "\n\t\x1b") {
		return tea.Key{}, fmt.Errorf("printable text must be a safe single-line value")
	}
	r, size := utf8.DecodeRuneInString(text)
	if r == utf8.RuneError && size == 1 {
		return tea.Key{}, fmt.Errorf("printable text is not valid UTF-8")
	}
	// Bubble Tea treats multi-rune pastes via Text; single grapheme typing uses
	// both Code and Text so Keystroke/Text paths remain usable.
	if utf8.RuneCountInString(text) == 1 {
		return tea.Key{Code: r, Text: text}, nil
	}
	return tea.Key{Code: tea.KeyExtended, Text: text}, nil
}
