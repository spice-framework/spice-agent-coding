package architectureproof

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"crypto/rand"

	"github.com/spice-framework/spice-agent-coding/internal/runidentity"
	"github.com/spice-framework/spice-agent/agent"
)

// NewIDSource contributes the production cryptographic identity source as the
// kernel's exact interface dependency.
//
// @Bean(name="architectureProofIDSource")
// @Singleton
func NewIDSource() (agent.IDSource, error) {
	return runidentity.NewSource(rand.Reader)
}
