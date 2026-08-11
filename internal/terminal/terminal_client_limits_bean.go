package terminal

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/client"
)

// NewClientLimits contributes explicit negotiation and replay budgets.
//
// @Bean(name="terminalClientLimits")
// @Singleton
func NewClientLimits() (client.Limits, error) {
	return client.NewLimits(4<<20, 512, 4096, 8<<20, 8, 64)
}
