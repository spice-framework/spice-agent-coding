package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

// NewEndpointScope selects the inseparable current-user runtime directory and
// local transport identity.
//
// @Bean(name="endpointScope")
// @Singleton
func NewEndpointScope() (endpoint.UserScope, error) {
	return endpoint.CurrentUserScope()
}
