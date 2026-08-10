package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

// NewEndpointScope selects the inseparable current-user runtime directory and
// local transport identity.
//
// @Bean(name="endpointScope")
func NewEndpointScope() (endpoint.UserScope, error) {
	return endpoint.CurrentUserScope()
}
