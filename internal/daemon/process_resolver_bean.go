package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent-coding/internal/processplatform"
	agentprocess "github.com/spice-framework/spice-agent/process"
)

// NewExecutableResolver contributes the distribution-owned, ambient-state-free
// executable resolver used by compiled shell tools.
//
// @Bean(name="processResolver")
// @Singleton
func NewExecutableResolver() agentprocess.ExecutableResolver {
	return processplatform.NewResolver()
}
