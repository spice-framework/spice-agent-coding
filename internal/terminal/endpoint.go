package terminal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spice-framework/spice-agent-coding/internal/daemonprocess"
	"github.com/spice-framework/spice-agent/client/localclient"
	"github.com/spice-framework/spice-agent/client/managed"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice/lifecycle"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

const (
	endpointPollInterval = 25 * time.Millisecond
	startupTimeout       = 10 * time.Second
	startupRetryInterval = 25 * time.Millisecond
	shutdownTimeout      = 10 * time.Second
)

// NewEndpointScope selects the inseparable current-user endpoint identity.
//
// @Bean(name="terminalEndpointScope")
func NewEndpointScope() (endpoint.UserScope, error) {
	return endpoint.CurrentUserScope()
}

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

// NewStartupLock binds cross-process attach-or-start serialization.
//
// @Bean(name="terminalStartupLock")
func NewStartupLock(store *endpoint.Store) (*localclient.StartupLock, error) {
	return localclient.NewStartupLock(store)
}

// NewDaemonStarter selects the sibling spice-agentd executable and bounded
// candidate ownership without starting it.
//
// @Bean(name="terminalDaemonStarter")
func NewDaemonStarter() (*daemonprocess.Starter, error) {
	return daemonprocess.New(daemonprocess.Config{
		StderrBytes: 64 << 10, GracefulTimeout: 5 * time.Second,
		TerminateDelay: 2 * time.Second,
	})
}

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
