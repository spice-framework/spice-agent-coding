package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"context"

	"github.com/spice-framework/spice-agent/client/localclient"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice/lifecycle"
)

// NewManagedDiscovery binds protected discovery without reading endpoint data.
//
// @Bean(name="terminalManagedDiscovery")
func NewManagedDiscovery(store *endpoint.Store) (*localclient.Discovery, lifecycle.Cleanup, error) {
	discovery, err := localclient.NewDiscovery(store)
	if err != nil {
		return nil, nil, err
	}
	return discovery, func(context.Context) error { return discovery.Close() }, nil
}
