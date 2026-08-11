package architectureproof

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"time"

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
	workspace, err := architectureProofWorkspaceFingerprint(codingConfig.Root)
	if err != nil {
		return nil, nil, err
	}
	options.WorkspaceFingerprint = workspace
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

func architectureProofWorkspaceFingerprint(root string) (string, error) {
	cleaned := filepath.Clean(root)
	if root == "" || cleaned == "." || !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("architecture-proof engine requires an absolute coding workspace")
	}
	digest := sha256.Sum256([]byte(cleaned))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
