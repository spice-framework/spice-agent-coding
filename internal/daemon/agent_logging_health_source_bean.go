package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	agentlogging "github.com/spice-framework/spice-agent/logging"
)

// NewAgentLoggingHealthSource preserves the logging readiness contribution
// alongside the distribution-owned runtime plugin health source.
//
// @Bean(name="agentLoggingHealth")
func NewAgentLoggingHealthSource(
	config agentlogging.Config,
	processor *agentlogging.Processor,
) agentdaemon.HealthSource {
	return agentlogging.NewHealthSource(config, processor)
}
