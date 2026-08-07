package grpcserver

import (
	"errors"
	"fmt"
	"slices"

	"github.com/spice-framework/spice-agent/client"
	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	"github.com/spice-framework/spice-agent/daemon"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
)

func buildToWire(build client.Build) (*commonv1.BuildIdentity, error) {
	if err := build.Validate(); err != nil {
		return nil, fmt.Errorf("server build: %w", err)
	}
	value := &commonv1.BuildIdentity{
		Component: build.Component(), Version: build.Version(),
		Commit: build.Commit(), GoVersion: build.GoVersion(),
	}
	if err := commonv1.ValidateBuildIdentity(value); err != nil {
		return nil, fmt.Errorf("server build wire value: %w", err)
	}
	return value, nil
}

func capabilitiesToWire(names []string) (*commonv1.CapabilitySet, error) {
	canonical := slices.Clone(names)
	slices.Sort(canonical)
	value := &commonv1.CapabilitySet{Names: canonical}
	if err := commonv1.ValidateCapabilities(value); err != nil {
		return nil, fmt.Errorf("server capabilities: %w", err)
	}
	return value, nil
}

func limitsToWire(limits client.Limits) (*commonv1.Limits, error) {
	if err := limits.Validate(); err != nil {
		return nil, fmt.Errorf("server limits: %w", err)
	}
	value := &commonv1.Limits{
		MaxMessageBytes: limits.MessageBytes(), MaxCollectionItems: limits.CollectionItems(),
		MaxReplayEvents: limits.ReplayEvents(), MaxReplayBytes: limits.ReplayBytes(),
		MaxConcurrentStreams: limits.ConcurrentStreams(), MaxActiveRuns: limits.ActiveRuns(),
	}
	if err := commonv1.ValidateLimits(value); err != nil {
		return nil, fmt.Errorf("server limits wire value: %w", err)
	}
	return value, nil
}

func healthToWire(health client.Health) (*commonv1.Health, error) {
	if err := health.Validate(); err != nil {
		return nil, fmt.Errorf("server health: %w", err)
	}
	state, err := healthStateToWire(health.State())
	if err != nil {
		return nil, err
	}
	limits, err := limitsToWire(health.Limits())
	if err != nil {
		return nil, err
	}
	value := &commonv1.Health{
		State: state, DegradedReasons: health.Reasons(),
		ActiveRuns: health.ActiveRuns(), Limits: limits,
	}
	if err = commonv1.ValidateHealth(value); err != nil {
		return nil, fmt.Errorf("server health wire value: %w", err)
	}
	return value, nil
}

func healthStateToWire(state client.HealthState) (commonv1.HealthState, error) {
	switch state {
	case client.HealthStarting:
		return commonv1.HealthState_HEALTH_STATE_STARTING, nil
	case client.HealthReady:
		return commonv1.HealthState_HEALTH_STATE_READY, nil
	case client.HealthDegraded:
		return commonv1.HealthState_HEALTH_STATE_DEGRADED, nil
	case client.HealthStopping:
		return commonv1.HealthState_HEALTH_STATE_STOPPING, nil
	default:
		return commonv1.HealthState_HEALTH_STATE_UNSPECIFIED, errors.New("server health state is unsupported")
	}
}

func definitionsToWire(set daemon.DefinitionSet, limits *commonv1.Limits) (*enginev1.DefinitionSet, error) {
	values := set.Definitions()
	definitions := make([]*enginev1.Definition, 0, len(values))
	for _, definition := range values {
		definitions = append(definitions, &enginev1.Definition{
			Id: definition.ID(), Revision: definition.Revision(),
			Model: definition.Agent().Model(), MaxTurns: definition.Agent().MaxTurns(),
		})
	}
	result := &enginev1.DefinitionSet{Revision: set.Revision(), Definitions: definitions}
	if err := enginev1.ValidateDefinitionSet(result, limits); err != nil {
		return nil, fmt.Errorf("server definition set: %w", err)
	}
	return result, nil
}
