package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"fmt"

	agentlogging "github.com/spice-framework/spice-agent/logging"
)

// NewAgentLoggingConfig replaces the Agent logging starter fallback with the
// distribution's generated, validated agent.logging configuration.
//
// @Bean(name="agentLoggingConfig")
// @Singleton
func NewAgentLoggingConfig(properties Properties) (agentlogging.Config, error) {
	config := agentlogging.DefaultConfig()
	config.MailboxCapacity = properties.LoggingMailboxCapacity
	config.IncludeProgress = properties.LoggingIncludeProgress
	config.ReadinessImpact = properties.LoggingReadinessImpact
	if err := config.Validate(); err != nil {
		return agentlogging.Config{}, fmt.Errorf("configure Agent logging: %w", err)
	}
	return config, nil
}
