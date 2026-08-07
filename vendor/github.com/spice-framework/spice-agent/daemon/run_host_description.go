package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/spice-framework/spice-agent/client"
)

// RunHostDescription is one immutable, validated view of the generated agent
// definitions and daemon readiness observed at the same host synchronization
// boundary. It is safe to expose before a client session is negotiated.
type RunHostDescription struct {
	definitions DefinitionSet
	health      client.Health
}

// NewRunHostDescription validates and defensively copies one host description.
func NewRunHostDescription(definitions DefinitionSet, health client.Health) (RunHostDescription, error) {
	canonicalDefinitions, err := NewDefinitionSet(definitions.Definitions())
	if err != nil {
		return RunHostDescription{}, fmt.Errorf("run host description definitions: %w", err)
	}
	if canonicalDefinitions.Revision() != definitions.Revision() {
		return RunHostDescription{}, errors.New("run host description definition revision is invalid")
	}
	if err = health.Validate(); err != nil {
		return RunHostDescription{}, fmt.Errorf("run host description health: %w", err)
	}
	return RunHostDescription{definitions: canonicalDefinitions, health: health}, nil
}

// Definitions returns a defensive copy of the generated definition catalog.
func (description RunHostDescription) Definitions() DefinitionSet {
	definitions, err := NewDefinitionSet(description.definitions.Definitions())
	if err != nil || definitions.Revision() != description.definitions.Revision() {
		return DefinitionSet{}
	}
	return definitions
}

// Health returns the immutable readiness snapshot paired with Definitions.
func (description RunHostDescription) Health() client.Health { return description.health }

// Validate verifies that this description still satisfies its public contract.
func (description RunHostDescription) Validate() error {
	_, err := NewRunHostDescription(description.definitions, description.health)
	return err
}

// Describe reports generated definitions and readiness without requiring a
// negotiated client session. It does not create or mutate session state.
func (host *RunHost) Describe(ctx context.Context) (RunHostDescription, error) {
	if host == nil {
		return RunHostDescription{}, ErrRunHostClosed
	}
	if ctx == nil {
		return RunHostDescription{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return RunHostDescription{}, err
	}
	description, err := host.snapshotDescription()
	if err != nil {
		return RunHostDescription{}, err
	}
	if err = ctx.Err(); err != nil {
		return RunHostDescription{}, err
	}
	return description, nil
}

// snapshotDescription captures all mutable host readiness fields at one lock
// boundary, then samples immutable passive health sources without holding the
// host mutex. Sources therefore cannot extend the lifecycle critical section.
func (host *RunHost) snapshotDescription() (RunHostDescription, error) {
	host.mu.Lock()
	healthSnapshot := host.healthSnapshotAssumingLocked()
	definitions := host.definitions
	host.mu.Unlock()
	health, err := healthSnapshot.health(host.healthSources)
	if err != nil {
		return RunHostDescription{}, ErrRunHostUnavailable
	}
	description, err := NewRunHostDescription(definitions, health)
	if err != nil {
		return RunHostDescription{}, ErrRunHostUnavailable
	}
	return description, nil
}
