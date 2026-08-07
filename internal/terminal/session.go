package terminal

import (
	"fmt"
	"path/filepath"

	"github.com/spice-framework/spice-agent-coding/internal/tuisession"
	agenttui "github.com/spice-framework/spice-agent-tui"
	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice/lifecycle"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// NewDefinition selects the coding agent advertised by the generated daemon.
//
// @Bean(name="terminalDefinition")
func NewDefinition() (client.DefinitionRef, error) {
	return client.NewDefinitionRef("coding", "v1")
}

// NewWorkspace constructs bounded initial presentation from typed properties.
//
// @Bean(name="terminalWorkspace")
func NewWorkspace(properties Properties) (agenttui.WorkspaceState, error) {
	workspace, err := filepath.Abs(properties.Workspace)
	if err != nil {
		return agenttui.WorkspaceState{}, fmt.Errorf("resolve terminal workspace: %w", err)
	}
	title, err := agenttui.NewText("Spice Agent — " + filepath.Base(filepath.Clean(workspace)))
	if err != nil {
		return agenttui.WorkspaceState{}, err
	}
	return agenttui.NewWorkspace(title, nil)
}

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

// NewIdentifierSource contributes the session's injected operation identity
// source without global mutable state.
//
// @Bean(name="terminalIdentifierSource")
func NewIdentifierSource() tuisession.IdentifierSource {
	return tuisession.RandomIdentifierSource{}
}

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
