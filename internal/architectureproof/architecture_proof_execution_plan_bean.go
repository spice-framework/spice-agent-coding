package architectureproof

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// NewExecutionPlanMetadata contributes the generated application's explicit
// portable-snapshot contract. The graph has no observer or dispatcher-decorator
// beans; no placeholder identity is invented for an absent executable.
//
// @Bean(name="architectureProofExecutionPlan")
func NewExecutionPlanMetadata() ExecutionPlanMetadata {
	return ExecutionPlanMetadata{
		CompiledPlanIdentities: []string{
			"broker:unavailable@" + agentModuleSelection + "#interaction.UnavailableBroker",
			"provider:architecture-proof-openai@" + providerModuleSelection + "#architectureproof.NewModelProvider",
			"stage:kernel@" + agentModuleSelection + "#agent.Engine",
			"stage:static-tool-plan-source@" + agentModuleSelection + "#stage.StaticToolPlanSource",
			"stage:tool-dispatcher@" + agentModuleSelection + "#stage.Dispatcher",
			"tool:read@" + toolsModuleSelection + "#autoconfigure.DefaultRead",
			"tool:replace@" + toolsModuleSelection + "#autoconfigure.DefaultReplace",
			"tool:shell@" + toolsModuleSelection + "#autoconfigure.DefaultShell",
		},
		SnapshotCompatibilityIdentity: snapshotCompatibilityIdentity,
	}
}
