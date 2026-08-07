// Package autoconfigure contributes the runtime-plugin host only when an
// application explicitly blank-imports this package. The contributed host
// augments the immutable compiled Spice tool graph; it never discovers or
// mutates compiled beans at runtime.
package autoconfigure

import (
	"errors"

	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
	"github.com/spice-framework/spice-agent/plugin/host/localendpoint"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"github.com/spice-framework/spice-agent/process"
	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
	"github.com/spice-framework/spice/lifecycle"
	"github.com/spice-framework/spice/starter"
)

// DefaultCompiledDispatcher snapshots the application-owned, statically
// composed named tool beans. Runtime plugins may augment this dispatcher in a
// new immutable generation, but cannot change the compiled Spice graph.
func DefaultCompiledDispatcher(tools map[string]tool.Tool) (stage.ToolDispatcher, error) {
	return stage.NewDispatcher(tools)
}

// DefaultCurrentUserEndpointFactory constructs the local-only endpoint
// allocator used for authenticated runtime-plugin processes.
func DefaultCurrentUserEndpointFactory() pluginhost.LocalEndpointFactory {
	return localendpoint.NewFactory()
}

// DefaultDisabledRestartPolicy preserves fail-closed, application-owned
// recovery selection. Blank-importing this package wires a valid zero policy;
// an application or distribution must explicitly replace it to enable
// automatic runtime-plugin recovery.
func DefaultDisabledRestartPolicy() pluginhost.RestartPolicy {
	return pluginhost.RestartPolicy{}
}

// DefaultHost constructs the runtime-plugin generation owner from exact,
// constructor-injected dependencies. Closing the returned lifecycle cleanup
// waits for active leases and releases every owned plugin process.
func DefaultHost(
	hostIdentity *pluginv1.BuildIdentity,
	compiled stage.ToolDispatcher,
	decorators []stage.ToolDispatchDecorator,
	restart pluginhost.RestartPolicy,
	launcher process.Launcher,
	endpoints pluginhost.LocalEndpointFactory,
) (*pluginhost.Host, lifecycle.Cleanup, error) {
	host, err := pluginhost.NewHost(pluginhost.HostConfig{
		HostIdentity: hostIdentity,
		Compiled:     compiled,
		Decorators:   decorators,
		Restart:      restart,
		Processes:    launcher,
		Endpoints:    endpoints,
	})
	if err != nil {
		return nil, nil, err
	}
	return host, host.Close, nil
}

// DefaultToolPlanSource exposes the exact interface consumed by the engine
// while preserving the Host pointer and its generation ownership.
func DefaultToolPlanSource(host *pluginhost.Host) (stage.ToolPlanSource, error) {
	if host == nil {
		return nil, errors.New("runtime plugin tool plan source requires a host")
	}
	return host, nil
}

// SpiceAutoConfiguration is statically decoded by Spice and never executed
// during analysis.
func SpiceAutoConfiguration() starter.AutoConfiguration {
	return starter.AutoConfiguration{
		Review: "docs/dependencies.md",
		Beans: []starter.AutoBean{
			{Factory: DefaultCompiledDispatcher, Name: "runtimePluginCompiledDispatcher", Fallback: true},
			{Factory: DefaultCurrentUserEndpointFactory, Name: "runtimePluginEndpointFactory", Fallback: true},
			{Factory: DefaultDisabledRestartPolicy, Name: "runtimePluginRestartPolicy", Fallback: true},
			{Factory: DefaultHost, Name: "runtimePluginHost", Fallback: true},
			{Factory: DefaultToolPlanSource, Name: "runtimePluginToolPlanSource", Fallback: true},
		},
	}
}

var _ stage.ToolPlanSource = (*pluginhost.Host)(nil)
