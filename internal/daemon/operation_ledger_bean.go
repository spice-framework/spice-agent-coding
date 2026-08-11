package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	agentdaemon "github.com/spice-framework/spice-agent/daemon"
)

// NewLedger constructs the bounded per-client idempotency ledger.
//
// @Bean(name="operationLedger")
// @Singleton
func NewLedger() (*agentdaemon.Ledger, error) {
	return agentdaemon.NewLedger(1024, 512)
}
