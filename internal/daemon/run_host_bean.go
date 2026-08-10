package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"fmt"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/client"
	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	"github.com/spice-framework/spice/lifecycle"
)

// NewRunHost composes the transport-independent engine service and registers
// its complete owned dependency shutdown as generated cleanup.
//
// @Bean(name="runHost")
func NewRunHost(
	root *Root,
	engine *agent.Engine,
	authority *agentdaemon.RunAuthority,
	sessions *agentdaemon.SessionStore,
	ledger *agentdaemon.Ledger,
	pending *agentdaemon.PendingHub,
	definitions agentdaemon.DefinitionSet,
	healthSources []agentdaemon.HealthSource,
	limits client.Limits,
) (*agentdaemon.RunHost, lifecycle.Cleanup, error) {
	ctx, err := rootContext(root)
	if err != nil {
		return nil, nil, err
	}
	host, err := agentdaemon.NewRunHost(agentdaemon.RunHostConfig{
		Root: ctx, Engine: engine, Authority: authority, Sessions: sessions,
		Ledger: ledger, Pending: pending, Definitions: definitions,
		HealthSources: healthSources, Limits: limits,
		TerminalRuns: 1024, TerminalBytes: 64 << 20,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("construct run host: %w", err)
	}
	return host, host.Shutdown, nil
}
