package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent-coding/internal/daemonprocess"
	"github.com/spice-framework/spice-agent-coding/internal/processplatform"
	agentprocess "github.com/spice-framework/spice-agent/process"
)

// NewProcessLauncher binds every compiled shell child to the daemon's adopted
// root containment boundary before the child can escape application ownership.
//
// @Bean(name="processLauncher")
// @Singleton
func NewProcessLauncher(registry daemonprocess.RootRegistry) (agentprocess.Launcher, error) {
	return processplatform.NewLauncher(registry)
}
