package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	agentdaemon "github.com/spice-framework/spice-agent/daemon"
)

// NewPendingHub constructs the bounded daemon interaction owner.
//
// @Bean(name="pendingHub")
// @Singleton
func NewPendingHub() (*agentdaemon.PendingHub, error) {
	return agentdaemon.NewPendingHub(agentdaemon.DefaultPendingLimits())
}
