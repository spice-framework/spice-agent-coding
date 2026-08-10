package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"context"
	"errors"

	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	agentprocess "github.com/spice-framework/spice-agent/process"
	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice/lifecycle"
)

// NewRuntimePluginHost owns the distribution's exact runtime-plugin Host bean.
// Its generated cleanup derives a fresh bounded context from the validated
// plugin lifecycle budgets, so a canceled startup context cannot skip process
// containment during rollback.
//
// @Bean(name="runtimePluginHost")
func NewRuntimePluginHost(
	hostIdentity *pluginv1.BuildIdentity,
	compiled stage.ToolDispatcher,
	decorators []stage.ToolDispatchDecorator,
	restart pluginhost.RestartPolicy,
	launcher agentprocess.Launcher,
	endpoints pluginhost.LocalEndpointFactory,
	plan RuntimePluginPlan,
) (*pluginhost.Host, lifecycle.Cleanup, error) {
	if err := plan.Validate(); err != nil {
		return nil, nil, errors.New("runtime plugin host requires a valid plan")
	}
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
	cleanup := func(ctx context.Context) error {
		parent := context.Background()
		if ctx != nil {
			parent = context.WithoutCancel(ctx)
		}
		operation, cancel := context.WithTimeout(parent, plan.cleanupTimeout)
		defer cancel()
		return host.Close(operation)
	}
	return host, cleanup, nil
}
