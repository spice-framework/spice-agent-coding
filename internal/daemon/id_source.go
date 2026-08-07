package daemon

import (
	"github.com/spice-framework/spice-agent-coding/internal/runidentity"
	"github.com/spice-framework/spice-agent/agent"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// NewIDSource contributes the production cryptographic identity source as the
// kernel's exact interface dependency.
//
// @Bean(name="daemonIDSource")
func NewIDSource() agent.IDSource {
	return runidentity.NewCrypto()
}
