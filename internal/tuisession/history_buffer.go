package tuisession

import (
	"slices"

	agenttui "github.com/spice-framework/spice-agent-tui"
)

type historyBuffer struct{}

func (historyBuffer) append(history []agenttui.Text, prompt agenttui.Text) []agenttui.Text {
	result := append(slices.Clone(history), prompt)
	if len(result) > agenttui.MaximumPromptHistoryItems {
		result = result[len(result)-agenttui.MaximumPromptHistoryItems:]
	}
	return result
}
