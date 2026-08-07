package architectureproof

import (
	"fmt"
	"time"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/interaction"
	"github.com/spice-framework/spice-agent/model"
	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
	"github.com/spice-framework/spice/lifecycle"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

const (
	agentModuleSelection    = "v0.0.0-20260807151358-4a1c8124e63f"
	providerModuleSelection = "v0.0.0-20260806230257-a6962fe2dabc"
	toolsModuleSelection    = "v0.0.0-20260807150540-eeacf58875c5"

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

// NewToolDispatcher creates the immutable base dispatch surface from the
// canonical generated named-tool map.
//
// @Bean(name="architectureProofToolDispatcher")
func NewToolDispatcher(tools map[string]tool.Tool) (stage.ToolDispatcher, error) {
	dispatcher, err := newToolDispatcher(tools)
	if err != nil {
		return nil, fmt.Errorf("construct architecture-proof dispatcher: %w", err)
	}
	return dispatcher, nil
}

func newToolDispatcher(tools map[string]tool.Tool) (stage.ToolDispatcher, error) {
	return stage.NewDispatcher(tools)
}

// NewToolPlanSource adapts the generated static dispatcher to the kernel's
// source-guaranteed immutable lease contract. Each run receives a fresh lease
// for the exact deterministic static generation.
//
// @Bean(name="architectureProofToolPlanSource")
func NewToolPlanSource(dispatcher stage.ToolDispatcher) (stage.ToolPlanSource, error) {
	source, err := stage.NewStaticToolPlanSource(dispatcher)
	if err != nil {
		return nil, err
	}
	return source, nil
}

// NewInteractionBroker selects the kernel's explicit unavailable fallback for
// this non-interactive proof.
//
// @Bean(name="architectureProofInteractionBroker")
func NewInteractionBroker() interaction.Broker {
	return interaction.UnavailableBroker{}
}

// NewEngine constructs the kernel from ordinary generated Spice dependencies.
// The kernel receives the plan source rather than rebuilding or discovering a
// dispatcher at runtime.
//
// @Bean(name="architectureProofEngine")
func NewEngine(
	provider model.Provider,
	toolPlans stage.ToolPlanSource,
	broker interaction.Broker,
	metadata ExecutionPlanMetadata,
) (*agent.Engine, lifecycle.Cleanup, error) {
	options := agent.DefaultEngineOptions()
	options.MetadataNamespaces = []string{"github.com/spice-framework/spice-agent-provider-openai"}
	options.CompiledPlanIdentities = append([]string(nil), metadata.CompiledPlanIdentities...)
	options.SnapshotCompatibilityIdentity = metadata.SnapshotCompatibilityIdentity
	engine, err := agent.NewEngineWithToolPlanSourceAndInteractionBroker(
		provider,
		toolPlans,
		broker,
		&agent.AtomicIDSource{},
		time.Now,
		nil,
		nil,
		options,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("construct architecture-proof engine: %w", err)
	}
	return engine, engine.Shutdown, nil
}
