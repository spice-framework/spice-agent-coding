package terminal

import (
	"context"
	"errors"
	"runtime"

	"github.com/spice-framework/spice-agent-coding/internal/distribution"
	"github.com/spice-framework/spice-agent-coding/internal/terminalconnector"
	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/client/managed"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice/lifecycle"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// NewClientConnector selects explicit attach or managed attach-or-start while
// preserving one statically generated client. Check mode constructs the
// managed connector but never initializes it.
//
// @Bean(name="terminalClientConnector")
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

// NewClientBuild contributes immutable terminal build provenance.
//
// @Bean(name="terminalClientBuild")
func NewClientBuild() (client.Build, error) {
	return client.NewBuild(
		distribution.TerminalComponent,
		distribution.Version,
		distribution.Commit,
		runtime.Version(),
	)
}

// NewClientProtocol declares the exact engine protocol supported by this
// distribution build.
//
// @Bean(name="terminalClientProtocol")
func NewClientProtocol() (client.ProtocolRange, error) {
	version, err := client.NewProtocolVersion(1, 3, 0)
	if err != nil {
		return client.ProtocolRange{}, err
	}
	return client.NewProtocolRange(version, version)
}

// NewClientLimits contributes explicit negotiation and replay budgets.
//
// @Bean(name="terminalClientLimits")
func NewClientLimits() (client.Limits, error) {
	return client.NewLimits(4<<20, 512, 4096, 8<<20, 8, 64)
}

// NewInitializeRequest contributes protocol-1.3 replay-safe initialization.
//
// @Bean(name="terminalInitializeRequest")
func NewInitializeRequest(
	protocol client.ProtocolRange,
	build client.Build,
	limits client.Limits,
) (client.InitializeRequest, error) {
	attempt, err := client.NewInitializationAttemptID()
	if err != nil {
		return client.InitializeRequest{}, err
	}
	capabilities := []string{"events", "snapshot-authority-v1", "snapshots"}
	return client.NewInitializeRequestWithAttempt(
		protocol,
		build,
		capabilities,
		[]string{"events"},
		limits,
		attempt,
	)
}
