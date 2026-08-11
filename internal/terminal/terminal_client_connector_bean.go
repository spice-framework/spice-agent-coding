package terminal

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"context"
	"errors"

	"github.com/spice-framework/spice-agent-coding/internal/terminalconnector"
	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/client/managed"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice/lifecycle"
)

// NewClientConnector selects explicit attach or managed attach-or-start while
// preserving one statically generated client. Check mode constructs the
// managed connector but never initializes it.
//
// @Bean(name="terminalClientConnector")
// @Singleton
func NewClientConnector(
	properties Properties,
	managedConnector *managed.Connector,
	store *endpoint.Store,
) (client.Connector, lifecycle.Cleanup, error) {
	switch properties.TerminalMode {
	case ModeManaged, ModeCheck:
		if properties.TerminalEndpoint != "" {
			return nil, nil, errors.New("managed terminal mode must not select an endpoint")
		}
		if managedConnector == nil {
			return nil, nil, errors.New("managed terminal connector is required")
		}
		return managedConnector, nil, nil
	case ModeAttach:
		connector, err := terminalconnector.NewExplicit(store, properties.TerminalEndpoint)
		if err != nil {
			return nil, nil, err
		}
		return connector, func(context.Context) error { return connector.Close() }, nil
	default:
		return nil, nil, errors.New("terminal mode is unsupported")
	}
}
