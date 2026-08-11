package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/client"
)

// NewProtocolVersion declares the highest engine protocol supported by this
// distribution build.
//
// @Bean(name="serverProtocol")
// @Singleton
func NewProtocolVersion() (client.ProtocolVersion, error) {
	return client.NewProtocolVersion(1, 3, 0)
}
