// Package autoconfigure contributes Agent event logging only when an
// application explicitly blank-imports it.
package autoconfigure

import (
	"github.com/spice-framework/spice-agent/daemon"
	"github.com/spice-framework/spice-agent/event"
	agentlogging "github.com/spice-framework/spice-agent/logging"
	"github.com/spice-framework/spice/lifecycle"
	spicelogging "github.com/spice-framework/spice/logging"
	"github.com/spice-framework/spice/starter"
)

// DefaultConfig contributes conservative bounded defaults.
func DefaultConfig() agentlogging.Config { return agentlogging.DefaultConfig() }

// DefaultMailbox contributes the sole best-effort Agent logging queue.
func DefaultMailbox(config agentlogging.Config) (*event.BestEffortObserver, error) {
	return agentlogging.NewMailbox(config)
}

// DefaultProcessor contributes the single consumer and generated cleanup.
func DefaultProcessor(
	config agentlogging.Config,
	mailbox *event.BestEffortObserver,
	logger *spicelogging.Logger,
) (*agentlogging.Processor, lifecycle.Cleanup, error) {
	return agentlogging.NewProcessor(config, mailbox, logger)
}

// DefaultHealthSource contributes optional fixed-code readiness state.
func DefaultHealthSource(config agentlogging.Config, processor *agentlogging.Processor) daemon.HealthSource {
	return agentlogging.NewHealthSource(config, processor)
}

// SpiceAutoConfiguration is statically decoded and never executed during
// analysis.
func SpiceAutoConfiguration() starter.AutoConfiguration {
	return starter.AutoConfiguration{
		Review: "docs/dependencies.md",
		Beans: []starter.AutoBean{
			{Factory: DefaultConfig, Name: "agentLoggingConfig", Fallback: true},
			{Factory: DefaultMailbox, Name: "agentLoggingMailbox", Fallback: true},
			{Factory: DefaultProcessor, Name: "agentLoggingProcessor", Fallback: true},
			{Factory: DefaultHealthSource, Name: "agentLoggingHealth", Fallback: true},
		},
	}
}
