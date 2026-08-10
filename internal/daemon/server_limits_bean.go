package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/client"
)

// NewLimits constructs the immutable server and negotiation budgets.
//
// @Bean(name="serverLimits")
func NewLimits() (client.Limits, error) {
	return client.NewLimits(4<<20, 512, 4096, 8<<20, 8, 64)
}
