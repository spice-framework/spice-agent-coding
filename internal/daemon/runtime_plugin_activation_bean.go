package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"errors"

	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
)

// NewRuntimePluginActivation constructs a side-effect-free lifecycle bean.
//
// @Bean(name="runtimePluginActivation")
// @Singleton
func NewRuntimePluginActivation(
	plan RuntimePluginPlan,
	host *pluginhost.Host,
) (*RuntimePluginActivation, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if host == nil {
		return nil, errors.New("runtime plugin activation requires a host")
	}
	return &RuntimePluginActivation{plan: plan, host: host}, nil
}
