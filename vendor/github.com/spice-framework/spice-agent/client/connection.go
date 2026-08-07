package client

import (
	"errors"
	"fmt"
	"slices"
)

// HealthState is the bounded readiness state visible to a local client.
type HealthState string

const (
	HealthStarting HealthState = "starting"
	HealthReady    HealthState = "ready"
	HealthDegraded HealthState = "degraded"
	HealthStopping HealthState = "stopping"
)

// Health is an immutable non-secret daemon readiness summary.
type Health struct {
	state      HealthState
	reasons    []string
	activeRuns uint64
	limits     Limits
}

// NewHealth validates and canonicalizes one readiness summary.
func NewHealth(state HealthState, reasons []string, activeRuns uint64, limits Limits) (Health, error) {
	switch state {
	case HealthStarting, HealthReady, HealthDegraded, HealthStopping:
	default:
		return Health{}, fmt.Errorf("health state %q is unsupported", state)
	}
	if err := limits.Validate(); err != nil {
		return Health{}, fmt.Errorf("health limits: %w", err)
	}
	if activeRuns > uint64(limits.ActiveRuns()) {
		return Health{}, errors.New("active run count exceeds the negotiated limit")
	}
	canonical, err := canonicalStrings("degraded reason", reasons, 64, maximumStatusBytes)
	if err != nil {
		return Health{}, err
	}
	if state != HealthDegraded && len(canonical) != 0 {
		return Health{}, errors.New("only degraded health may contain degraded reasons")
	}
	if state == HealthDegraded && len(canonical) == 0 {
		return Health{}, errors.New("degraded health requires at least one reason")
	}
	return Health{state: state, reasons: canonical, activeRuns: activeRuns, limits: limits}, nil
}

func (health Health) State() HealthState { return health.state }
func (health Health) Reasons() []string  { return slices.Clone(health.reasons) }
func (health Health) ActiveRuns() uint64 { return health.activeRuns }
func (health Health) Limits() Limits     { return health.limits }

func (health Health) Validate() error {
	_, err := NewHealth(health.state, health.reasons, health.activeRuns, health.limits)
	return err
}

// DefinitionRef identifies one exact generated agent definition.
type DefinitionRef struct {
	id       string
	revision string
}

// NewDefinitionRef constructs an exact definition reference.
func NewDefinitionRef(id, revision string) (DefinitionRef, error) {
	if err := token("definition ID", id, maximumTokenBytes); err != nil {
		return DefinitionRef{}, err
	}
	if err := token("definition revision", revision, maximumTokenBytes); err != nil {
		return DefinitionRef{}, err
	}
	return DefinitionRef{id: id, revision: revision}, nil
}

func (reference DefinitionRef) ID() string       { return reference.id }
func (reference DefinitionRef) Revision() string { return reference.revision }

func (reference DefinitionRef) Validate() error {
	_, err := NewDefinitionRef(reference.id, reference.revision)
	return err
}

// Definition is one immutable server-owned generated agent definition.
type Definition struct {
	reference DefinitionRef
	model     string
	maxTurns  uint32
}

// NewDefinition constructs one definition advertised by a server.
func NewDefinition(reference DefinitionRef, model string, maxTurns uint32) (Definition, error) {
	if err := reference.Validate(); err != nil {
		return Definition{}, err
	}
	if err := token("definition model", model, maximumTokenBytes); err != nil {
		return Definition{}, err
	}
	if maxTurns == 0 || maxTurns > 1000 {
		return Definition{}, errors.New("definition max turns must be between 1 and 1000")
	}
	return Definition{reference: reference, model: model, maxTurns: maxTurns}, nil
}

func (definition Definition) Ref() DefinitionRef { return definition.reference }
func (definition Definition) Model() string      { return definition.model }
func (definition Definition) MaxTurns() uint32   { return definition.maxTurns }

func (definition Definition) Validate() error {
	_, err := NewDefinition(definition.reference, definition.model, definition.maxTurns)
	return err
}

// Catalog is a canonical immutable generated definition set.
type Catalog struct {
	revision    string
	definitions []Definition
}

// NewCatalog validates and sorts definitions within negotiated collection limits.
func NewCatalog(revision string, definitions []Definition, limits Limits) (Catalog, error) {
	if err := token("catalog revision", revision, maximumTokenBytes); err != nil {
		return Catalog{}, err
	}
	if err := limits.Validate(); err != nil {
		return Catalog{}, fmt.Errorf("catalog limits: %w", err)
	}
	if len(definitions) == 0 || uint64(len(definitions)) > uint64(limits.CollectionItems()) {
		return Catalog{}, fmt.Errorf("definition count must be between 1 and %d", limits.CollectionItems())
	}
	result := slices.Clone(definitions)
	for index := range result {
		if err := result[index].Validate(); err != nil {
			return Catalog{}, fmt.Errorf("definition %d: %w", index, err)
		}
	}
	slices.SortFunc(result, func(left, right Definition) int {
		if left.reference.id == right.reference.id {
			return compareString(left.reference.revision, right.reference.revision)
		}
		return compareString(left.reference.id, right.reference.id)
	})
	for index := 1; index < len(result); index++ {
		if result[index-1].reference == result[index].reference {
			return Catalog{}, errors.New("catalog definitions must be unique by ID and revision")
		}
	}
	return Catalog{revision: revision, definitions: result}, nil
}

func (catalog Catalog) Revision() string { return catalog.revision }
func (catalog Catalog) Definitions() []Definition {
	return slices.Clone(catalog.definitions)
}

// Find returns the exact definition, if present.
func (catalog Catalog) Find(reference DefinitionRef) (Definition, bool) {
	for _, definition := range catalog.definitions {
		if definition.reference == reference {
			return definition, true
		}
	}
	return Definition{}, false
}

func (catalog Catalog) Validate(limits Limits) error {
	_, err := NewCatalog(catalog.revision, catalog.definitions, limits)
	return err
}

// ConnectionSpec contains non-secret inputs used to construct Connection.
// Slice fields are defensively copied by NewConnection.
type ConnectionSpec struct {
	Protocol       ProtocolVersion
	Server         Build
	Capabilities   []string
	Limits         Limits
	Health         Health
	ClientID       string
	OwnershipEpoch uint64
	Catalog        Catalog
}

// Connection is an immutable negotiated session contract.
type Connection struct {
	protocol       ProtocolVersion
	server         Build
	capabilities   []string
	limits         Limits
	health         Health
	clientID       string
	ownershipEpoch uint64
	catalog        Catalog
}

// NewConnection constructs one complete negotiated session contract.
func NewConnection(spec ConnectionSpec) (Connection, error) {
	if err := spec.Protocol.Validate(); err != nil {
		return Connection{}, err
	}
	if err := spec.Server.Validate(); err != nil {
		return Connection{}, err
	}
	capabilities, err := canonicalTokens("capability", spec.Capabilities)
	if err != nil {
		return Connection{}, err
	}
	if err := spec.Limits.Validate(); err != nil {
		return Connection{}, err
	}
	if err := spec.Health.Validate(); err != nil {
		return Connection{}, err
	}
	if err := token("client ID", spec.ClientID, 128); err != nil {
		return Connection{}, err
	}
	if spec.OwnershipEpoch == 0 {
		return Connection{}, errors.New("ownership epoch must be positive")
	}
	if err := spec.Catalog.Validate(spec.Limits); err != nil {
		return Connection{}, err
	}
	return Connection{
		protocol: spec.Protocol, server: spec.Server, capabilities: capabilities,
		limits: spec.Limits, health: spec.Health, clientID: spec.ClientID,
		ownershipEpoch: spec.OwnershipEpoch, catalog: spec.Catalog,
	}, nil
}

func (connection Connection) Protocol() ProtocolVersion { return connection.protocol }
func (connection Connection) Server() Build             { return connection.server }
func (connection Connection) Capabilities() []string    { return slices.Clone(connection.capabilities) }
func (connection Connection) Limits() Limits            { return connection.limits }
func (connection Connection) Health() Health            { return connection.health }
func (connection Connection) ClientID() string          { return connection.clientID }
func (connection Connection) OwnershipEpoch() uint64    { return connection.ownershipEpoch }
func (connection Connection) Catalog() Catalog          { return connection.catalog }

func (connection Connection) Validate() error {
	_, err := NewConnection(ConnectionSpec{
		Protocol: connection.protocol, Server: connection.server,
		Capabilities: connection.capabilities, Limits: connection.limits,
		Health: connection.health, ClientID: connection.clientID,
		OwnershipEpoch: connection.ownershipEpoch, Catalog: connection.catalog,
	})
	return err
}

func compareString(left, right string) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
