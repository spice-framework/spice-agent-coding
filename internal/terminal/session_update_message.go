package terminal

import agenttui "github.com/spice-framework/spice-agent-tui"

type sessionUpdateMessage struct {
	update agenttui.SessionUpdate
	err    error
}
