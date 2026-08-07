package daemon

import (
	"github.com/spice-framework/spice-agent-coding/internal/daemonprocess"
	"github.com/spice-framework/spice-agent-coding/internal/processplatform"
	agentprocess "github.com/spice-framework/spice-agent/process"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// NewExecutableResolver contributes the distribution-owned, ambient-state-free
// executable resolver used by compiled shell tools.
//
// @Bean(name="processResolver")
func NewExecutableResolver() agentprocess.ExecutableResolver {
	return processplatform.NewResolver()
}

// NewProcessLauncher binds every compiled shell child to the daemon's adopted
// root containment boundary before the child can escape application ownership.
//
// @Bean(name="processLauncher")
func NewProcessLauncher(registry daemonprocess.RootRegistry) (agentprocess.Launcher, error) {
	return processplatform.NewLauncher(registry)
}
