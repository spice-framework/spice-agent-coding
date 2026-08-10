package daemon

import (
	"context"
	"sync"

	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"
// @import { OnStart } from "github.com/spice-framework/spice/annotation/lifecycle"

// RuntimePluginActivation owns one explicit Host activation attempt before
// daemon transport publication. It never discovers executables or mutates the
// generated Spice bean graph.
type RuntimePluginActivation struct {
	plan RuntimePluginPlan
	host *pluginhost.Host

	mu    sync.RWMutex
	state runtimePluginActivationState
}

// Start activates the configured complete Set once. Optional failure preserves
// the Host's compiled generation and becomes fixed-code degraded health;
// required failure prevents the dependent Runtime lifecycle from publishing.
//
// @OnStart
func (activation *RuntimePluginActivation) Start(ctx context.Context) error {
	if activation == nil || ctx == nil {
		return ErrRuntimePluginActivationPending
	}
	activation.mu.Lock()
	if activation.state != runtimePluginActivationNew {
		activation.mu.Unlock()
		return ErrRuntimePluginActivationPending
	}
	activation.state = runtimePluginActivationStarting
	activation.mu.Unlock()

	if !activation.plan.Enabled() {
		activation.setState(runtimePluginActivationReady)
		return nil
	}
	_, err := activation.host.Activate(ctx, activation.plan.Set())
	if err == nil {
		activation.setState(runtimePluginActivationReady)
		return nil
	}
	if cause := context.Cause(ctx); cause != nil {
		activation.setState(runtimePluginActivationFailed)
		return cause
	}
	if activation.plan.Required() {
		activation.setState(runtimePluginActivationFailed)
		return ErrRuntimePluginRequiredUnavailable
	}
	activation.setState(runtimePluginActivationDegraded)
	return nil
}

// PublicationReady gates the daemon's listener before any endpoint can be
// opened or published.
func (activation *RuntimePluginActivation) PublicationReady() error {
	if activation == nil {
		return ErrRuntimePluginActivationPending
	}
	activation.mu.RLock()
	defer activation.mu.RUnlock()
	switch activation.state {
	case runtimePluginActivationReady, runtimePluginActivationDegraded:
		return nil
	case runtimePluginActivationFailed:
		return ErrRuntimePluginRequiredUnavailable
	default:
		return ErrRuntimePluginActivationPending
	}
}

func (activation *RuntimePluginActivation) setState(state runtimePluginActivationState) {
	activation.mu.Lock()
	activation.state = state
	activation.mu.Unlock()
}

func (activation *RuntimePluginActivation) healthContribution() agentdaemon.HealthContribution {
	if activation == nil {
		return (runtimePluginHealthPolicy{}).contribution(agentdaemon.HealthReasonDependencyUnavailable)
	}
	activation.mu.RLock()
	state := activation.state
	activation.mu.RUnlock()
	switch state {
	case runtimePluginActivationReady:
		return agentdaemon.HealthContribution{}
	case runtimePluginActivationDegraded:
		return (runtimePluginHealthPolicy{}).contribution(agentdaemon.HealthReasonDependencyDegraded)
	default:
		return (runtimePluginHealthPolicy{}).contribution(agentdaemon.HealthReasonDependencyUnavailable)
	}
}
