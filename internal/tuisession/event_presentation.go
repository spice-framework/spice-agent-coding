package tuisession

import (
	"strings"
	"unicode"
	"unicode/utf8"

	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent/client"
)

type eventPresentation struct{}

func (eventPresentation) summary(event client.Event) string {
	detail := event.Detail()
	if value, available := detail.Text(); available {
		return value
	}
	if value, available := detail.Status(); available {
		return string(event.Kind()) + ": " + value
	}
	if failure, available := detail.ModelFailure(); available {
		return string(event.Kind()) + ": " + failure.Message()
	}
	if _, name, available := detail.ToolStart(); available {
		return string(event.Kind()) + ": " + name
	}
	if _, message, available := detail.ToolProgress(); available {
		return string(event.Kind()) + ": " + message
	}
	if terminal, available := detail.ToolTerminal(); available {
		return string(event.Kind()) + ": " + terminal.Name() + " " + terminal.Problem()
	}
	if _, kind, available := detail.InteractionStart(); available {
		return string(event.Kind()) + ": " + kind
	}
	if _, status, available := detail.InteractionTerminal(); available {
		return string(event.Kind()) + ": " + status
	}
	return string(event.Kind())
}

func (eventPresentation) runTerminal(kind client.EventKind) bool {
	return kind == client.EventRunCompleted || kind == client.EventRunFailed || kind == client.EventRunCancelled
}

func (eventPresentation) text(value string) (agenttui.Text, error) {
	value = strings.ToValidUTF8(value, "�")
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return -1
		}
		return character
	}, value)
	if len(value) > agenttui.MaximumTextBytes {
		value = value[:agenttui.MaximumTextBytes]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return agenttui.NewText(value)
}
