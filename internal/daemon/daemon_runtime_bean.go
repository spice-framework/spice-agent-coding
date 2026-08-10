package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"context"
	"errors"
	"fmt"

	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice-agent/daemon/grpcserver"
)

// NewRuntime receives every runtime dependency through generated Spice
// constructor injection.
//
// @Bean(name="daemonRuntime")
func NewRuntime(
	scope endpoint.UserScope,
	store *endpoint.Store,
	token endpoint.Token,
	build client.Build,
	protocol client.ProtocolVersion,
	server *grpcserver.Server,
	activation *RuntimePluginActivation,
	listenerFactory ListenerFactory,
) (*Runtime, error) {
	if store == nil || server == nil || activation == nil || listenerFactory == nil {
		return nil, errors.New("daemon runtime requires endpoint store, gRPC server, runtime plugin activation, and listener factory")
	}
	if err := scope.Validate(); err != nil {
		return nil, fmt.Errorf("validate endpoint scope: %w", err)
	}
	if err := token.Validate(); err != nil {
		return nil, err
	}
	if err := build.Validate(); err != nil {
		return nil, err
	}
	if err := protocol.Validate(); err != nil {
		return nil, err
	}
	runtime := &Runtime{
		scope: scope, token: token, build: build,
		protocol: protocol, server: server, activation: activation,
		serveDone: make(chan struct{}),
	}
	runtime.services = runtimeServices{
		listen: listenerFactory.Listen,
		publish: func(ctx context.Context, metadata endpoint.Metadata) (runtimePublication, error) {
			return store.Publish(ctx, metadata)
		},
		metadata: runtime.endpointMetadata,
	}
	return runtime, nil
}
