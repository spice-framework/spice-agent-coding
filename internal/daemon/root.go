package daemon

import (
	"context"
	"errors"

	"github.com/spice-framework/spice-agent-coding/internal/daemonprocess"
	"github.com/spice-framework/spice/lifecycle"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// Root owns the daemon service lifetime independently from request contexts.
type Root struct {
	context.Context //nolint:containedctx // this bean is the application service lifetime.
}

// NewRootRegistry adopts the managed launcher's containment channel before
// any child-capable bean is constructed. Explicit serve receives an inert
// implementation. Construction fails closed when a managed channel is present
// but cannot be authenticated.
//
// @Bean(name="daemonRootRegistry")
func NewRootRegistry() (daemonprocess.RootRegistry, lifecycle.Cleanup, error) {
	registry, err := daemonprocess.AdoptRootRegistry()
	if err != nil {
		return nil, nil, err
	}
	if registry == nil {
		return nil, nil, errors.New("daemon root registry is unavailable")
	}
	cleanup := func(context.Context) error { return registry.Close() }
	return registry, cleanup, nil
}

// NewRoot creates the application-owned cancellation root. Generated cleanup
// cancels it only after lifecycle stop hooks have drained the transport.
//
// @Bean(name="daemonRoot")
func NewRoot(registry daemonprocess.RootRegistry) (*Root, lifecycle.Cleanup, error) {
	if registry == nil {
		return nil, nil, errors.New("daemon root registry is unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	root := &Root{Context: ctx}
	cleanup := func(context.Context) error {
		cancel()
		return nil
	}
	return root, cleanup, nil
}

func rootContext(root *Root) (context.Context, error) {
	if root == nil || root.Context == nil {
		return nil, errors.New("daemon root is unavailable")
	}
	if err := root.Err(); err != nil {
		return nil, errors.New("daemon root is already canceled")
	}
	return root.Context, nil
}
