package tuittest

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	agenttui "github.com/spice-framework/spice-agent-tui"
)

// ValidateStatusSemantics proves that status meaning remains present in plain
// text and therefore does not depend on the styled frame's color.
func (screen Screen) ValidateStatusSemantics() error {
	level := agenttui.StatusLevel(screen.statusLevel)
	if !supportedStatusLevel(level) {
		return fmt.Errorf("screen has unsupported status level %q", screen.statusLevel)
	}
	label := "[" + strings.ToUpper(string(level)) + "]"
	if !strings.Contains(screen.plain, label) {
		return fmt.Errorf("plain screen does not contain status label %q", label)
	}
	return nil
}

// ValidateAccessibility checks the semantic accessible-output contract. It
// rejects terminal controls and requires status meaning without relying on
// color, alternate-screen state, or cursor control.
func (screen Screen) ValidateAccessibility() error {
	if !screen.accessible {
		return errors.New("screen was not captured in accessible mode")
	}
	if screen.altScreen {
		return errors.New("accessible screen requests alternate-screen mode")
	}
	if screen.cursorVisible {
		return errors.New("accessible screen requests cursor control")
	}
	if err := validateAccessibleText("styled", screen.styled); err != nil {
		return err
	}
	if err := validateAccessibleText("plain", screen.plain); err != nil {
		return err
	}
	if err := screen.ValidateStatusSemantics(); err != nil {
		return err
	}
	meaning := "[" + strings.ToUpper(screen.statusLevel) + "] " + screen.status
	if screen.status != "" && !strings.Contains(screen.plain, meaning) {
		return fmt.Errorf("accessible plain screen does not contain status meaning %q", meaning)
	}
	return nil
}

func validateAccessibleText(name, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("accessible %s output is not valid UTF-8", name)
	}
	if strings.Contains(value, "<ESC>") {
		return fmt.Errorf("accessible %s output contains a normalized escape token", name)
	}
	for offset, character := range value {
		if character != '\n' && unicode.IsControl(character) {
			return fmt.Errorf("accessible %s output contains control U+%04X at byte %d", name, character, offset)
		}
	}
	return nil
}

func supportedStatusLevel(level agenttui.StatusLevel) bool {
	switch level {
	case agenttui.StatusReady, agenttui.StatusBusy, agenttui.StatusDisconnected,
		agenttui.StatusReconnecting, agenttui.StatusWarning, agenttui.StatusError:
		return true
	default:
		return false
	}
}
