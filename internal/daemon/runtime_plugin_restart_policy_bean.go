package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"errors"

	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
)

// NewRuntimePluginRestartPolicy explicitly replaces the core auto-
// configuration's disabled exact-type fallback with the bounded production
// policy selected by this distribution.
//
// @Bean(name="runtimePluginRestartPolicy")
// @Singleton
func NewRuntimePluginRestartPolicy(plan RuntimePluginPlan) (pluginhost.RestartPolicy, error) {
	if err := plan.Validate(); err != nil {
		return pluginhost.RestartPolicy{}, errors.New("runtime plugin restart policy requires a valid plan")
	}
	if !plan.Enabled() {
		return pluginhost.RestartPolicy{}, nil
	}
	return pluginhost.DefaultRestartPolicy(), nil
}
