package daemon

import (
	"time"

	"github.com/spice-framework/spice-agent/agent"
	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	"github.com/spice-framework/spice-agent/interaction"
	"github.com/spice-framework/spice-agent/model"
	"github.com/spice-framework/spice-agent/stage"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

const snapshotCompatibilityIdentity = "github.com/spice-framework/spice-agent-coding/daemon:v1"

// NewPendingHub constructs the bounded daemon interaction owner.
//
// @Bean(name="pendingHub")
func NewPendingHub() (*agentdaemon.PendingHub, error) {
	return agentdaemon.NewPendingHub(agentdaemon.DefaultPendingLimits())
}

// NewInteractionBroker exposes the same pending hub through the exact kernel
// interface without a registry or implicit interface scan.
//
// @Bean(name="daemonInteractionBroker")
func NewInteractionBroker(pending *agentdaemon.PendingHub) interaction.Broker {
	return pending
}

// NewEngine constructs the deterministic kernel from generated dependencies.
// RunHost owns its orderly shutdown.
//
// @Bean(name="daemonEngine")
func NewEngine(
	provider model.Provider,
	toolPlans stage.ToolPlanSource,
	broker interaction.Broker,
) (*agent.Engine, error) {
	options := agent.DefaultEngineOptions()
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
		&agent.AtomicIDSource{},
		time.Now,
		nil,
		nil,
		options,
	)
}
