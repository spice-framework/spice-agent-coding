package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"time"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/interaction"
	"github.com/spice-framework/spice-agent/model"
	"github.com/spice-framework/spice-agent/stage"
)

// NewEngine constructs the deterministic kernel from generated dependencies.
// RunHost owns its orderly shutdown.
//
// @Bean(name="daemonEngine")
func NewEngine(
	provider model.Provider,
	toolPlans stage.ToolPlanSource,
	broker interaction.Broker,
	ids agent.IDSource,
	limits client.Limits,
) (*agent.Engine, error) {
	options := agent.DefaultEngineOptions()
	logLimits, err := (engineLogPolicy{}).limits(limits)
	if err != nil {
		return nil, err
	}
	options.LogLimits = logLimits
	options.MetadataNamespaces = []string{"github.com/spice-framework/spice-agent-provider-openai"}
	options.CompiledPlanIdentities = []string{
		"broker:pending-hub",
		"provider:openai-responses",
		"stage:kernel",
		"stage:runtime-plugin-compiled-dispatcher",
		"stage:runtime-plugin-host",
		"stage:runtime-plugin-tool-plan-source",
		"tool:read",
		"tool:replace",
		"tool:shell",
	}
	options.SnapshotCompatibilityIdentity = snapshotCompatibilityIdentity
	return agent.NewEngineWithToolPlanSourceAndInteractionBroker(
		provider,
		toolPlans,
		broker,
		ids,
		time.Now,
		nil,
		nil,
		options,
	)
}
