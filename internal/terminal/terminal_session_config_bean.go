package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent-coding/internal/tuisession"
	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent/client"
)

// NewSessionConfig freezes the client and presentation inputs selected by the
// generated graph.
//
// @Bean(name="terminalSessionConfig")
func NewSessionConfig(
	initialize client.InitializeRequest,
	definition client.DefinitionRef,
	workspace agenttui.WorkspaceState,
	status agenttui.StatusState,
) (tuisession.Config, error) {
	return tuisession.NewConfig(initialize, definition, workspace, status)
}
