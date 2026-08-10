package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

// NewEndpointToken generates one process-lifetime authentication credential.
//
// @Bean(name="endpointToken")
func NewEndpointToken() (endpoint.Token, error) {
	return endpoint.GenerateToken()
}
