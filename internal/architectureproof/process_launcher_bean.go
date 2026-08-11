package architectureproof

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent-coding/internal/processplatform"
	agentprocess "github.com/spice-framework/spice-agent/process"
)

// NewProcessLauncher contributes a self-owned launcher for the embedded proof.
//
// @Bean(name="processLauncher")
// @Singleton
func NewProcessLauncher() (agentprocess.Launcher, error) {
	return processplatform.NewLauncher(proofChildRegistrar{})
}
