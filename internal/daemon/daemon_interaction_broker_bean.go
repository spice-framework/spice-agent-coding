package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	"github.com/spice-framework/spice-agent/interaction"
)

// NewInteractionBroker exposes the same pending hub through the exact kernel
// interface without a registry or implicit interface scan.
//
// @Bean(name="daemonInteractionBroker")
// @Singleton
func NewInteractionBroker(pending *agentdaemon.PendingHub) interaction.Broker {
	return pending
}
