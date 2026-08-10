package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"context"
	"errors"

	"github.com/spice-framework/spice-agent-coding/internal/daemonprocess"
	"github.com/spice-framework/spice-agent/client/localclient"
	"github.com/spice-framework/spice-agent/client/managed"
	"github.com/spice-framework/spice/lifecycle"
)

// NewManagedConnector composes secure discovery, serialized startup, and exact
// owned-candidate cleanup without performing I/O.
//
// @Bean(name="terminalManagedConnector")
func NewManagedConnector(
	discovery *localclient.Discovery,
	starter *daemonprocess.Starter,
	lock *localclient.StartupLock,
) (*managed.Connector, lifecycle.Cleanup, error) {
	if discovery == nil || starter == nil || lock == nil {
		return nil, nil, errors.New("managed terminal dependencies are required")
	}
	connector, err := managed.New(managed.Config{
		Discovery: discovery, Starter: starter, StartupLock: lock,
		StartupTimeout: startupTimeout, RetryInterval: startupRetryInterval,
		ShutdownTimeout: shutdownTimeout,
	})
	if err != nil {
		return nil, nil, err
	}
	return connector, func(context.Context) error { return connector.Close() }, nil
}
