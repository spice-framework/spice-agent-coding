package daemon

import (
	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
)

type runtimePluginHealthSource struct {
	activation *RuntimePluginActivation
	host       *pluginhost.Host
}

func (source *runtimePluginHealthSource) HealthContribution() agentdaemon.HealthContribution {
	contribution := source.activation.healthContribution()
	if len(contribution.Reasons()) != 0 {
		return contribution
	}
	policy := runtimePluginHealthPolicy{}
	switch source.host.Health().State() {
	case pluginhost.HealthStateReady:
		return agentdaemon.HealthContribution{}
	case pluginhost.HealthStateDegraded:
		return policy.contribution(agentdaemon.HealthReasonDependencyDegraded)
	case pluginhost.HealthStateRecovering:
		return policy.contribution(agentdaemon.HealthReasonDependencyRecovering)
	default:
		return policy.contribution(agentdaemon.HealthReasonDependencyUnavailable)
	}
}
