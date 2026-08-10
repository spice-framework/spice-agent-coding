package architectureproof

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"fmt"
	"time"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/interaction"
	"github.com/spice-framework/spice-agent/model"
	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice/lifecycle"
)

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
	ids agent.IDSource,
) (*agent.Engine, lifecycle.Cleanup, error) {
	options := agent.DefaultEngineOptions()
	options.MetadataNamespaces = []string{"github.com/spice-framework/spice-agent-provider-openai"}
	options.CompiledPlanIdentities = append([]string(nil), metadata.CompiledPlanIdentities...)
	options.SnapshotCompatibilityIdentity = metadata.SnapshotCompatibilityIdentity
	engine, err := agent.NewEngineWithToolPlanSourceAndInteractionBroker(
		provider,
		toolPlans,
		broker,
		ids,
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
