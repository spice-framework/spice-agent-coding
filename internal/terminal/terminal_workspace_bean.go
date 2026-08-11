package terminal

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"fmt"
	"path/filepath"

	workspaceconfig "github.com/spice-framework/spice-agent-coding/internal/workspace"
	agenttui "github.com/spice-framework/spice-agent-tui"
)

// NewWorkspace constructs bounded initial presentation from typed properties.
//
// @Bean(name="terminalWorkspace")
// @Singleton
func NewWorkspace(properties workspaceconfig.Properties) (agenttui.WorkspaceState, error) {
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
