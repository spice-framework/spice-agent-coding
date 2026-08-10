package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"context"
	"fmt"
	"time"

	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice/lifecycle"
)

// NewEndpointStore opens protected coordination state without publishing an
// endpoint.
//
// @Bean(name="endpointStore")
func NewEndpointStore(scope endpoint.UserScope) (*endpoint.Store, lifecycle.Cleanup, error) {
	store, err := scope.OpenStore(25 * time.Millisecond)
	if err != nil {
		return nil, nil, fmt.Errorf("open endpoint store: %w", err)
	}
	return store, func(context.Context) error { return store.Close() }, nil
}
