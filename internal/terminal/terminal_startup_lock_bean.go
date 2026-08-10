package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/client/localclient"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

// NewStartupLock binds cross-process attach-or-start serialization.
//
// @Bean(name="terminalStartupLock")
func NewStartupLock(store *endpoint.Store) (*localclient.StartupLock, error) {
	return localclient.NewStartupLock(store)
}
