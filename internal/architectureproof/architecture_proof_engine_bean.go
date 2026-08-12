package architectureproof

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"fmt"
	"time"

	workspaceconfig "github.com/spice-framework/spice-agent-coding/internal/workspace"
	coding "github.com/spice-framework/spice-agent-tools-coding"
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
// @Singleton
func NewEngine(
	provider model.Provider,
	toolPlans stage.ToolPlanSource,
	broker interaction.Broker,
	metadata ExecutionPlanMetadata,
	ids agent.IDSource,
	codingConfig coding.Config,
) (*agent.Engine, lifecycle.Cleanup, error) {
	options := agent.DefaultEngineOptions()
	options.MetadataNamespaces = []string{"github.com/spice-framework/spice-agent-provider-openai"}
	options.CompiledPlanIdentities = append([]string(nil), metadata.CompiledPlanIdentities...)
	options.SnapshotCompatibilityIdentity = metadata.SnapshotCompatibilityIdentity
	workspaceFingerprint, err := workspaceconfig.NewFingerprint(codingConfig.Root)
	if err != nil {
		return nil, nil, fmt.Errorf("construct architecture-proof engine: %w", err)
	}
	options.WorkspaceFingerprint = workspaceFingerprint.String()
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
