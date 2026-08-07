package daemon

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/client"
	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice-agent/daemon/grpcserver"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"github.com/spice-framework/spice/lifecycle"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

const (
	daemonComponent = "spice-agentd"
	daemonVersion   = "0.1.0-preview.1-dev"
	daemonCommit    = "development"
)

// NewEndpointScope selects the inseparable current-user runtime directory and
// local transport identity.
//
// @Bean(name="endpointScope")
func NewEndpointScope() (endpoint.UserScope, error) {
	return endpoint.CurrentUserScope()
}

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

// NewEndpointToken generates one process-lifetime authentication credential.
//
// @Bean(name="endpointToken")
func NewEndpointToken() (endpoint.Token, error) {
	return endpoint.GenerateToken()
}

// NewServerBuild returns non-secret immutable daemon provenance.
//
// @Bean(name="serverBuild")
func NewServerBuild() (client.Build, error) {
	return client.NewBuild(daemonComponent, daemonVersion, daemonCommit, runtime.Version())
}

// NewRuntimePluginHostIdentity adapts the daemon's single validated build
// provenance value to the Protobuf identity used only at the runtime-plugin
// process boundary. It neither discovers nor launches plugins.
//
// @Bean(name="runtimePluginHostIdentity")
func NewRuntimePluginHostIdentity(build client.Build) (*pluginv1.BuildIdentity, error) {
	if err := build.Validate(); err != nil {
		return nil, fmt.Errorf("construct runtime plugin host identity: %w", err)
	}
	identity := &pluginv1.BuildIdentity{
		Component: build.Component(),
		Version:   build.Version(),
		Commit:    build.Commit(),
		Runtime:   build.GoVersion(),
	}
	if err := pluginv1.ValidateBuildIdentity(identity); err != nil {
		return nil, fmt.Errorf("validate runtime plugin host identity: %w", err)
	}
	return identity, nil
}

// NewProtocolVersion declares the highest engine protocol supported by this
// distribution build.
//
// @Bean(name="serverProtocol")
func NewProtocolVersion() (client.ProtocolVersion, error) {
	return client.NewProtocolVersion(1, 3, 0)
}

// NewLimits constructs the immutable server and negotiation budgets.
//
// @Bean(name="serverLimits")
func NewLimits() (client.Limits, error) {
	return client.NewLimits(4<<20, 512, 4096, 8<<20, 8, 64)
}

// NewSessionStore constructs bounded client ownership rooted in the daemon
// lifetime.
//
// @Bean(name="sessionStore")
func NewSessionStore(root *Root) (*agentdaemon.SessionStore, error) {
	ctx, err := rootContext(root)
	if err != nil {
		return nil, err
	}
	return agentdaemon.NewSessionStore(ctx, 1024)
}

// NewLedger constructs the bounded per-client idempotency ledger.
//
// @Bean(name="operationLedger")
func NewLedger() (*agentdaemon.Ledger, error) {
	return agentdaemon.NewLedger(1024, 512)
}

// NewRunAuthority opens the current-user persistent snapshot authority and
// registers its retained directory handle for generated reverse cleanup.
//
// @Bean(name="runAuthority")
func NewRunAuthority(
	properties Properties,
) (*agentdaemon.RunAuthority, lifecycle.Cleanup, error) {
	authority, err := agentdaemon.NewRunAuthority(agentdaemon.RunAuthorityConfig{
		Directory: properties.RunAuthorityDirectory,
	})
	if err != nil {
		return nil, nil, err
	}
	cleanup := func(context.Context) error { return authority.Close() }
	return authority, cleanup, nil
}

// NewDefinitionSet constructs the generated server-owned agent catalog.
//
// @Bean(name="definitionSet")
func NewDefinitionSet(properties Properties) (agentdaemon.DefinitionSet, error) {
	definition, err := agent.NewDefinition("coding", properties.Model, 32)
	if err != nil {
		return agentdaemon.DefinitionSet{}, fmt.Errorf("construct coding definition: %w", err)
	}
	value, err := agentdaemon.NewDefinition("coding", "v1", definition)
	if err != nil {
		return agentdaemon.DefinitionSet{}, err
	}
	return agentdaemon.NewDefinitionSet([]agentdaemon.Definition{value})
}

// NewRunHost composes the transport-independent engine service and registers
// its complete owned dependency shutdown as generated cleanup.
//
// @Bean(name="runHost")
func NewRunHost(
	root *Root,
	engine *agent.Engine,
	authority *agentdaemon.RunAuthority,
	sessions *agentdaemon.SessionStore,
	ledger *agentdaemon.Ledger,
	pending *agentdaemon.PendingHub,
	definitions agentdaemon.DefinitionSet,
	limits client.Limits,
) (*agentdaemon.RunHost, lifecycle.Cleanup, error) {
	ctx, err := rootContext(root)
	if err != nil {
		return nil, nil, err
	}
	host, err := agentdaemon.NewRunHost(agentdaemon.RunHostConfig{
		Root: ctx, Engine: engine, Authority: authority, Sessions: sessions,
		Ledger: ledger, Pending: pending, Definitions: definitions, Limits: limits,
		TerminalRuns: 1024, TerminalBytes: 64 << 20,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("construct run host: %w", err)
	}
	return host, host.Shutdown, nil
}

// NewGRPCServer constructs the authenticated protocol boundary without
// opening or publishing a listener.
//
// @Bean(name="grpcServer")
func NewGRPCServer(
	root *Root,
	token endpoint.Token,
	host *agentdaemon.RunHost,
	sessions *agentdaemon.SessionStore,
	build client.Build,
) (*grpcserver.Server, error) {
	ctx, err := rootContext(root)
	if err != nil {
		return nil, err
	}
	return grpcserver.NewServer(grpcserver.ServerConfig{
		Root: ctx, EndpointToken: token, Host: host, Sessions: sessions,
		Build:           build,
		Capabilities:    []string{"events", "snapshot-authority-v1", "snapshots"},
		MaximumSessions: 1024,
	})
}
