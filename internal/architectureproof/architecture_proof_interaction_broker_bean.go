package architectureproof

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/interaction"
)

// NewInteractionBroker selects the kernel's explicit unavailable fallback for
// this non-interactive proof.
//
// @Bean(name="architectureProofInteractionBroker")
// @Singleton
func NewInteractionBroker() interaction.Broker {
	return interaction.UnavailableBroker{}
}
