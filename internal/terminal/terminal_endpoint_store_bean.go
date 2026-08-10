package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"context"
	"fmt"

	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice/lifecycle"
)

// NewEndpointStore opens protected coordination state without discovery.
//
// @Bean(name="terminalEndpointStore")
func NewEndpointStore(scope endpoint.UserScope) (*endpoint.Store, lifecycle.Cleanup, error) {
	store, err := scope.OpenStore(endpointPollInterval)
	if err != nil {
		return nil, nil, fmt.Errorf("open terminal endpoint store: %w", err)
	}
	return store, func(context.Context) error { return store.Close() }, nil
}
