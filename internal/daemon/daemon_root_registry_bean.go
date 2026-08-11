package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"context"
	"errors"

	"github.com/spice-framework/spice-agent-coding/internal/daemonprocess"
	"github.com/spice-framework/spice/lifecycle"
)

// NewRootRegistry adopts the managed launcher's containment channel before
// any child-capable bean is constructed. Explicit serve receives an inert
// implementation. Construction fails closed when a managed channel is present
// but cannot be authenticated.
//
// @Bean(name="daemonRootRegistry")
// @Singleton
func NewRootRegistry() (daemonprocess.RootRegistry, lifecycle.Cleanup, error) {
	registry, err := (daemonprocess.RootRegistryFactory{}).Adopt()
	if err != nil {
		return nil, nil, err
	}
	if registry == nil {
		return nil, nil, errors.New("daemon root registry is unavailable")
	}
	cleanup := func(context.Context) error { return registry.Close() }
	return registry, cleanup, nil
}
