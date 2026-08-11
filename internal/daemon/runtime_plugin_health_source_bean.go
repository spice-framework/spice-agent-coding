package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"errors"

	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
)

// NewRuntimePluginHealthSource adapts owned in-memory activation/Host state to
// the daemon's fixed-code passive health contract.
//
// @Bean(name="runtimePluginHealthSource")
// @Singleton
func NewRuntimePluginHealthSource(
	activation *RuntimePluginActivation,
	host *pluginhost.Host,
) (agentdaemon.HealthSource, error) {
	if activation == nil || host == nil {
		return nil, errors.New("runtime plugin health source requires activation and host")
	}
	return &runtimePluginHealthSource{activation: activation, host: host}, nil
}
