package daemon

import agentdaemon "github.com/spice-framework/spice-agent/daemon"

type runtimePluginHealthPolicy struct{}

func (runtimePluginHealthPolicy) contribution(reason agentdaemon.HealthReasonCode) agentdaemon.HealthContribution {
	contribution, err := agentdaemon.NewHealthContribution([]agentdaemon.HealthReasonCode{reason})
	if err != nil {
		panic("invalid fixed runtime plugin health reason")
	}
	return contribution
}
