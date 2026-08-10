package tuisession

import agenttui "github.com/spice-framework/spice-agent-tui"

type commandResultFactory struct{}

func (commandResultFactory) new(value string) (agenttui.CommandResult, error) {
	text, err := (eventPresentation{}).text(value)
	if err != nil {
		return agenttui.CommandResult{}, err
	}
	return agenttui.NewCommandResult(text, nil)
}
