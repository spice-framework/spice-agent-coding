package daemon

import (
	"fmt"
	"time"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/client"
	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	"github.com/spice-framework/spice-agent/event"
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
	ids agent.IDSource,
	limits client.Limits,
) (*agent.Engine, error) {
	options := agent.DefaultEngineOptions()
	logLimits, err := engineLogLimits(limits)
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

func engineLogLimits(limits client.Limits) (event.LogLimits, error) {
	maxEvents := int(limits.ReplayEvents())
	if maxEvents < 1 || uint64(maxEvents) != uint64(limits.ReplayEvents()) {
		return event.LogLimits{}, fmt.Errorf(
			"construct daemon event log: replay event limit %d does not fit this platform",
			limits.ReplayEvents(),
		)
	}
	maxBytes := int(limits.ReplayBytes()) // #nosec G115 -- exact positive round-trip validation follows immediately.
	if maxBytes < 1 || uint64(maxBytes) != limits.ReplayBytes() {
		return event.LogLimits{}, fmt.Errorf(
			"construct daemon event log: replay byte limit %d does not fit this platform",
			limits.ReplayBytes(),
		)
	}
	logLimits := event.DefaultLogLimits()
	if maxEvents > logLimits.MaxEvents || maxBytes > logLimits.MaxBytes {
		return event.LogLimits{}, fmt.Errorf(
			"construct daemon event log: replay limit %d events/%d bytes exceeds retained history %d events/%d bytes",
			maxEvents,
			maxBytes,
			logLimits.MaxEvents,
			logLimits.MaxBytes,
		)
	}
	logLimits.SubscriberMaxEvents = maxEvents
	logLimits.SubscriberMaxBytes = maxBytes
	return logLimits, nil
}
