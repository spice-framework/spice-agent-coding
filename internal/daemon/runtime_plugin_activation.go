package daemon

import (
	"context"
	"errors"
	"sync"

	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"
// @import { OnStart } from "github.com/spice-framework/spice/annotation/lifecycle"

var (
	// ErrRuntimePluginRequiredUnavailable is the fixed, secret-safe startup
	// failure returned when a required configured plugin cannot be activated.
	ErrRuntimePluginRequiredUnavailable = errors.New("required runtime plugin is unavailable")
	// ErrRuntimePluginActivationPending prevents transport publication before
	// the generated activation lifecycle has reached a terminal admission state.
	ErrRuntimePluginActivationPending = errors.New("runtime plugin activation is incomplete")
)

type runtimePluginActivationState uint8

const (
	runtimePluginActivationNew runtimePluginActivationState = iota
	runtimePluginActivationStarting
	runtimePluginActivationReady
	runtimePluginActivationDegraded
	runtimePluginActivationFailed
)

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
		return runtimePluginHealth(agentdaemon.HealthReasonDependencyUnavailable)
	}
	activation.mu.RLock()
	state := activation.state
	activation.mu.RUnlock()
	switch state {
	case runtimePluginActivationReady:
		return agentdaemon.HealthContribution{}
	case runtimePluginActivationDegraded:
		return runtimePluginHealth(agentdaemon.HealthReasonDependencyDegraded)
	default:
		return runtimePluginHealth(agentdaemon.HealthReasonDependencyUnavailable)
	}
}

type runtimePluginHealthSource struct {
	activation *RuntimePluginActivation
	host       *pluginhost.Host
}

func (source *runtimePluginHealthSource) HealthContribution() agentdaemon.HealthContribution {
	contribution := source.activation.healthContribution()
	if len(contribution.Reasons()) != 0 {
		return contribution
	}
	switch source.host.Health().State() {
	case pluginhost.HealthStateReady:
		return agentdaemon.HealthContribution{}
	case pluginhost.HealthStateDegraded:
		return runtimePluginHealth(agentdaemon.HealthReasonDependencyDegraded)
	case pluginhost.HealthStateRecovering:
		return runtimePluginHealth(agentdaemon.HealthReasonDependencyRecovering)
	default:
		return runtimePluginHealth(agentdaemon.HealthReasonDependencyUnavailable)
	}
}

func runtimePluginHealth(reason agentdaemon.HealthReasonCode) agentdaemon.HealthContribution {
	contribution, err := agentdaemon.NewHealthContribution([]agentdaemon.HealthReasonCode{reason})
	if err != nil {
		panic("invalid fixed runtime plugin health reason")
	}
	return contribution
}

var _ agentdaemon.HealthSource = (*runtimePluginHealthSource)(nil)
