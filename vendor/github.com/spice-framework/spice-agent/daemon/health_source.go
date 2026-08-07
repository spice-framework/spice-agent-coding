package daemon

import (
	"errors"
	"fmt"
	"slices"
)

const (
	// MaximumHealthSources bounds passive readiness work for one snapshot.
	MaximumHealthSources = 32
	// MaximumHealthContributionReasons bounds one source before aggregation.
	MaximumHealthContributionReasons = 16
)

// HealthReasonCode is a fixed, secret-safe daemon readiness reason. The
// allowlist is intentionally closed: sources cannot return arbitrary error,
// path, endpoint, provider, or plugin-controlled text through daemon health.
type HealthReasonCode string

const (
	HealthReasonDependencyDegraded    HealthReasonCode = "dependency_degraded"
	HealthReasonDependencyRecovering  HealthReasonCode = "dependency_recovering"
	HealthReasonDependencyUnavailable HealthReasonCode = "dependency_unavailable"
)

// HealthContribution is one immutable passive readiness observation. Its zero
// value is a ready contribution.
type HealthContribution struct {
	reasons []HealthReasonCode
}

// NewHealthContribution validates, sorts, deduplicates, and defensively copies
// fixed reason codes. Input is bounded before deduplication so a source cannot
// make health work proportional to an unbounded caller-controlled slice.
func NewHealthContribution(reasons []HealthReasonCode) (HealthContribution, error) {
	if len(reasons) > MaximumHealthContributionReasons {
		return HealthContribution{}, fmt.Errorf(
			"health contribution reasons exceed %d", MaximumHealthContributionReasons,
		)
	}
	canonical := slices.Clone(reasons)
	for _, reason := range canonical {
		if err := validateHealthReasonCode(reason); err != nil {
			return HealthContribution{}, err
		}
	}
	slices.Sort(canonical)
	canonical = slices.Compact(canonical)
	return HealthContribution{reasons: canonical}, nil
}

// Reasons returns a defensive copy of the canonical fixed reason codes.
func (contribution HealthContribution) Reasons() []HealthReasonCode {
	return slices.Clone(contribution.reasons)
}

// Validate verifies that a contribution is canonical and bounded.
func (contribution HealthContribution) Validate() error {
	canonical, err := NewHealthContribution(contribution.reasons)
	if err != nil {
		return err
	}
	if !slices.Equal(canonical.reasons, contribution.reasons) {
		return errors.New("health contribution reasons are not canonical")
	}
	return nil
}

func validateHealthReasonCode(reason HealthReasonCode) error {
	switch reason {
	case HealthReasonDependencyDegraded,
		HealthReasonDependencyRecovering,
		HealthReasonDependencyUnavailable:
		return nil
	default:
		return errors.New("health reason code is unsupported")
	}
}

// HealthSource supplies one passive, non-blocking readiness contribution.
// Implementations must inspect already-owned in-memory state only. RunHost
// invokes sources without holding its state mutex and contains a source panic
// as the fixed dependency_unavailable reason.
type HealthSource interface {
	HealthContribution() HealthContribution
}

func cloneHealthSources(sources []HealthSource) ([]HealthSource, error) {
	if len(sources) > MaximumHealthSources {
		return nil, fmt.Errorf("run host health sources exceed %d", MaximumHealthSources)
	}
	cloned := slices.Clone(sources)
	for _, source := range cloned {
		if source == nil {
			return nil, errors.New("run host health source is nil")
		}
	}
	return cloned, nil
}

func unavailableHealthContribution() HealthContribution {
	return HealthContribution{reasons: []HealthReasonCode{HealthReasonDependencyUnavailable}}
}

func sampleHealthSource(source HealthSource) (contribution HealthContribution) {
	defer func() {
		if recover() != nil {
			contribution = unavailableHealthContribution()
		}
	}()
	contribution = source.HealthContribution()
	if contribution.Validate() != nil {
		return unavailableHealthContribution()
	}
	return contribution
}
