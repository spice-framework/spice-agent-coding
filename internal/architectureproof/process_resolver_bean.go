package architectureproof

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent-coding/internal/processplatform"
	agentprocess "github.com/spice-framework/spice-agent/process"
)

// NewExecutableResolver contributes the same native resolver used by the
// distribution daemon.
//
// @Bean(name="processResolver")
// @Singleton
func NewExecutableResolver() agentprocess.ExecutableResolver {
	return processplatform.NewResolver()
}
