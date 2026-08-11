package terminal

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

// NewEndpointScope selects the inseparable current-user endpoint identity.
//
// @Bean(name="terminalEndpointScope")
// @Singleton
func NewEndpointScope() (endpoint.UserScope, error) {
	return endpoint.CurrentUserScope()
}
