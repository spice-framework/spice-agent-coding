package logging

import "github.com/spice-framework/spice-agent/daemon"

// HealthSource is an optional passive fixed-code readiness contribution.
type HealthSource struct {
	processor *Processor
	enabled   bool
}

// NewHealthSource constructs an in-memory-only health adapter.
func NewHealthSource(config Config, processor *Processor) *HealthSource {
	return &HealthSource{processor: processor, enabled: config.ReadinessImpact}
}

// HealthContribution never exposes handler errors or event payload text.
func (source *HealthSource) HealthContribution() daemon.HealthContribution {
	if source == nil || !source.enabled {
		return daemon.HealthContribution{}
	}
	if source.processor == nil {
		return healthContribution(daemon.HealthReasonDependencyUnavailable)
	}
	snapshot := source.processor.Snapshot()
	if snapshot.Closed() && snapshot.LogFailures() != 0 {
		return healthContribution(daemon.HealthReasonDependencyUnavailable)
	}
	if snapshot.LogFailures() != 0 || snapshot.DecodeFailures() != 0 || snapshot.OverflowDropped() != 0 {
		return healthContribution(daemon.HealthReasonDependencyDegraded)
	}
	return daemon.HealthContribution{}
}

func healthContribution(reason daemon.HealthReasonCode) daemon.HealthContribution {
	contribution, _ := daemon.NewHealthContribution([]daemon.HealthReasonCode{reason})
	return contribution
}

var _ daemon.HealthSource = (*HealthSource)(nil)
