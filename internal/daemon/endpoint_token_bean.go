package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

// NewEndpointToken generates one process-lifetime authentication credential.
//
// @Bean(name="endpointToken")
// @Singleton
func NewEndpointToken() (endpoint.Token, error) {
	return endpoint.GenerateToken()
}
