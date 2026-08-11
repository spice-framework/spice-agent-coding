package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"

	coding "github.com/spice-framework/spice-agent-tools-coding"
	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/event"
	"github.com/spice-framework/spice-agent/interaction"
	agentlogging "github.com/spice-framework/spice-agent/logging"
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
	codingConfig coding.Config,
	loggingProcessor *agentlogging.Processor,
	bestEffortObservers []*event.BestEffortObserver,
) (*agent.Engine, error) {
	if loggingProcessor == nil {
		return nil, errors.New("daemon engine requires the Agent logging processor")
	}
	options := agent.DefaultEngineOptions()
	logLimits, err := (engineLogPolicy{}).limits(limits)
	if err != nil {
		return nil, err
	}
	options.LogLimits = logLimits
	options.MetadataNamespaces = []string{"github.com/spice-framework/spice-agent-provider-openai"}
	options.CompiledPlanIdentities = []string{
		"broker:pending-hub",
		"observer:agent-logging",
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
	workspace, err := workspaceFingerprint(codingConfig.Root)
	if err != nil {
		return nil, err
	}
	options.WorkspaceFingerprint = workspace
	return agent.NewEngineWithToolPlanSourceAndInteractionBroker(
		provider,
		toolPlans,
		broker,
		ids,
		time.Now,
		nil,
		bestEffortObservers,
		options,
	)
}

func workspaceFingerprint(root string) (string, error) {
	cleaned := filepath.Clean(root)
	if root == "" || cleaned == "." || !filepath.IsAbs(cleaned) {
		return "", errors.New("daemon engine requires an absolute coding workspace")
	}
	digest := sha256.Sum256([]byte(cleaned))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
