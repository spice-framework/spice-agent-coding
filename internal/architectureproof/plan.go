package architectureproof

import (
	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

const (
	agentModuleSelection    = "v0.1.0-preview.4"
	providerModuleSelection = "v0.1.0-preview.1"
	toolsModuleSelection    = "v0.1.0-preview.1"

	// snapshotCompatibilityIdentity is application-owned semantic compatibility,
	// not a hash of machine paths or timestamps. Changing executable snapshot
	// semantics requires a new value.
	snapshotCompatibilityIdentity = "github.com/spice-framework/spice-agent-coding/architectureproof:v1"
)

// ExecutionPlanMetadata is immutable application-owned input to the generated
// engine bean. Every identity names the executable implementation and selected
// module version represented by this generated Spice graph.
type ExecutionPlanMetadata struct {
	CompiledPlanIdentities        []string
	SnapshotCompatibilityIdentity string
}

func newToolDispatcher(tools map[string]tool.Tool) (stage.ToolDispatcher, error) {
	return stage.NewDispatcher(tools)
}
