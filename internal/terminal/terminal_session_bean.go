package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent-coding/internal/tuisession"
	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice/lifecycle"
)

// NewSession adapts the negotiated Agent protocol to the UI-neutral session
// contract and registers its complete worker/stream cleanup with Spice.
//
// @Bean(name="terminalSession")
func NewSession(
	config tuisession.Config,
	connector client.Connector,
	identifiers tuisession.IdentifierSource,
) (agenttui.Session, lifecycle.Cleanup, error) {
	return tuisession.New(config, connector, identifiers)
}
