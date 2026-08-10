package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"context"
	"errors"

	"github.com/spice-framework/spice-agent-coding/internal/daemonprocess"
	"github.com/spice-framework/spice/lifecycle"
)

// NewRoot creates the application-owned cancellation root. Generated cleanup
// cancels it only after lifecycle stop hooks have drained the transport.
//
// @Bean(name="daemonRoot")
func NewRoot(registry daemonprocess.RootRegistry) (*Root, lifecycle.Cleanup, error) {
	if registry == nil {
		return nil, nil, errors.New("daemon root registry is unavailable")
	}
	root := &Root{done: make(chan struct{})}
	cleanup := func(context.Context) error {
		root.cancel()
		return nil
	}
	return root, cleanup, nil
}
