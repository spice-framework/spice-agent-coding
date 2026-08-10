package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	agenttui "github.com/spice-framework/spice-agent-tui"
)

// NewInitialStatus constructs the pre-negotiation status snapshot.
//
// @Bean(name="terminalInitialStatus")
func NewInitialStatus() (agenttui.StatusState, error) {
	message, err := agenttui.NewText("connecting to local Spice Agent daemon")
	if err != nil {
		return agenttui.StatusState{}, err
	}
	return agenttui.NewStatus(agenttui.StatusReconnecting, message, nil)
}
