package terminal

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/client"
)

// NewClientProtocol declares the exact engine protocol supported by this
// distribution build.
//
// @Bean(name="terminalClientProtocol")
// @Singleton
func NewClientProtocol() (client.ProtocolRange, error) {
	version, err := client.NewProtocolVersion(1, 3, 0)
	if err != nil {
		return client.ProtocolRange{}, err
	}
	return client.NewProtocolRange(version, version)
}
