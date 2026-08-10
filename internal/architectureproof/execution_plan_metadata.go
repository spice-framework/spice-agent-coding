package architectureproof

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// ExecutionPlanMetadata is immutable application-owned input to the generated
// engine bean. Every identity names the executable implementation and selected
// module version represented by this generated Spice graph.
type ExecutionPlanMetadata struct {
	CompiledPlanIdentities        []string
	SnapshotCompatibilityIdentity string
}
