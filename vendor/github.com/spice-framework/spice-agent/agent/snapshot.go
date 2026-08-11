package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/spice-framework/spice-agent/interaction"
	"github.com/spice-framework/spice-agent/message"
	"github.com/spice-framework/spice-agent/stage"
)

const (
	SnapshotVersion         = "spice.agent.snapshot/v1alpha3"
	MaximumSnapshotBytes    = 16 << 20
	maximumSnapshotMessages = 4096
	maximumPlanIdentities   = 512
	maximumInteractionIDs   = 4096
)

// LifecycleStatus identifies one safe persisted run boundary.
type LifecycleStatus string

const (
	LifecycleSuspended LifecycleStatus = "suspended"
	LifecycleCompleted LifecycleStatus = "completed"
	LifecycleFailed    LifecycleStatus = "failed"
	LifecycleCancelled LifecycleStatus = "cancelled"
)

// Snapshot is immutable provider-neutral run state. It intentionally excludes
// contexts, services, credentials, processes, clients, and mutable registries.
type Snapshot struct {
	version        string
	runID          string
	definition     Definition
	completedTurns uint32
	history        []message.Message
	planIdentity   PlanIdentity
	interactionIDs []interaction.ID
	lastSequence   uint64
	status         LifecycleStatus
}

// NewSnapshot constructs and fully validates one safe-state snapshot.
func NewSnapshot(runID string, definition Definition, completedTurns uint32, history []message.Message, planIdentity PlanIdentity, lastSequence uint64, status LifecycleStatus) (Snapshot, error) {
	return newSnapshot(runID, definition, completedTurns, history, planIdentity, nil, lastSequence, status)
}

func newSnapshot(runID string, definition Definition, completedTurns uint32, history []message.Message, planIdentity PlanIdentity, interactionIDs []interaction.ID, lastSequence uint64, status LifecycleStatus) (Snapshot, error) {
	result := Snapshot{
		version: SnapshotVersion, runID: runID, definition: definition,
		completedTurns: completedTurns, history: cloneHistory(history),
		planIdentity:   planIdentity.clone(),
		interactionIDs: append([]interaction.ID(nil), interactionIDs...),
		lastSequence:   lastSequence, status: status,
	}
	if err := result.Validate(); err != nil {
		return Snapshot{}, err
	}
	return result, nil
}

// Validate rejects corrupted, active, or uncertain provider-neutral state.
func (snapshot Snapshot) Validate() error {
	if snapshot.version != SnapshotVersion {
		return fmt.Errorf("agent snapshot version %q is unsupported", snapshot.version)
	}
	if err := snapshotToken("run ID", snapshot.runID, 96); err != nil {
		return err
	}
	if _, err := NewDefinition(snapshot.definition.name, snapshot.definition.model, snapshot.definition.maxTurns); err != nil {
		return err
	}
	if snapshot.completedTurns > snapshot.definition.maxTurns {
		return errors.New("agent snapshot completed turns exceed the definition maximum")
	}
	if len(snapshot.history) == 0 || len(snapshot.history) > maximumSnapshotMessages {
		return fmt.Errorf("agent snapshot message count must be between 1 and %d", maximumSnapshotMessages)
	}
	if err := snapshot.planIdentity.Validate(); err != nil {
		return err
	}
	if snapshot.lastSequence == 0 || snapshot.lastSequence == math.MaxUint64 {
		return errors.New("agent snapshot last sequence must be positive and resumable")
	}
	switch snapshot.status {
	case LifecycleSuspended:
		if snapshot.completedTurns == 0 || snapshot.completedTurns >= snapshot.definition.maxTurns {
			return errors.New("agent suspended snapshot has no remaining turn")
		}
	case LifecycleCompleted, LifecycleFailed, LifecycleCancelled:
	default:
		return fmt.Errorf("agent snapshot lifecycle status %q is unsafe", snapshot.status)
	}
	if err := validateInteractionIDs(snapshot.interactionIDs); err != nil {
		return err
	}
	return validateSnapshotHistory(snapshot.history)
}

func (snapshot Snapshot) Version() string        { return snapshot.version }
func (snapshot Snapshot) RunID() string          { return snapshot.runID }
func (snapshot Snapshot) Definition() Definition { return snapshot.definition }
func (snapshot Snapshot) CompletedTurns() uint32 { return snapshot.completedTurns }
func (snapshot Snapshot) PlanIdentity() PlanIdentity {
	return snapshot.planIdentity.clone()
}
func (snapshot Snapshot) ToolPlanID() stage.PlanID { return snapshot.planIdentity.ToolPlanID() }
func (snapshot Snapshot) InteractionIDs() []interaction.ID {
	return append([]interaction.ID(nil), snapshot.interactionIDs...)
}
func (snapshot Snapshot) LastSequence() uint64       { return snapshot.lastSequence }
func (snapshot Snapshot) Status() LifecycleStatus    { return snapshot.status }
func (snapshot Snapshot) History() []message.Message { return cloneHistory(snapshot.history) }

// MarshalBinary encodes deterministic bounded snapshot JSON.
func (snapshot Snapshot) MarshalBinary() ([]byte, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	wire, err := snapshot.toWire()
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode agent snapshot: %w", err)
	}
	if len(encoded) > MaximumSnapshotBytes {
		return nil, fmt.Errorf("agent snapshot exceeds %d bytes", MaximumSnapshotBytes)
	}
	return encoded, nil
}

// ParseSnapshot decodes exactly one versioned snapshot with unknown fields rejected.
func ParseSnapshot(encoded []byte) (Snapshot, error) {
	if len(encoded) == 0 || len(encoded) > MaximumSnapshotBytes {
		return Snapshot{}, fmt.Errorf("agent snapshot must be between 1 and %d bytes", MaximumSnapshotBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var wire snapshotWire
	if err := decoder.Decode(&wire); err != nil {
		return Snapshot{}, fmt.Errorf("decode agent snapshot: %w", err)
	}
	if err := ensureSnapshotEOF(decoder); err != nil {
		return Snapshot{}, err
	}
	if wire.Version != SnapshotVersion {
		return Snapshot{}, fmt.Errorf("agent snapshot version %q is unsupported", wire.Version)
	}
	history := make([]message.Message, len(wire.History))
	for index, value := range wire.History {
		decoded, err := decodeSnapshotMessage(value)
		if err != nil {
			return Snapshot{}, fmt.Errorf("decode snapshot message %d: %w", index, err)
		}
		history[index] = decoded
	}
	definition, err := NewDefinition(wire.Definition.Name, wire.Definition.Model, wire.Definition.MaxTurns)
	if err != nil {
		return Snapshot{}, err
	}
	interactionIDs := make([]interaction.ID, len(wire.InteractionIDs))
	for index, value := range wire.InteractionIDs {
		interactionIDs[index] = interaction.ID(value)
	}
	planIdentity, err := reconstructPlanIdentity(
		wire.PlanIdentity.CompiledIdentities,
		wire.PlanIdentity.SnapshotCompatibilityIdentity,
		wire.PlanIdentity.WorkspaceFingerprint,
		wire.PlanIdentity.ToolPlanID,
		wire.PlanIdentity.Fingerprint,
	)
	if err != nil {
		return Snapshot{}, err
	}
	result, err := newSnapshot(wire.RunID, definition, wire.CompletedTurns, history, planIdentity, interactionIDs, wire.LastSequence, wire.Status)
	if err != nil {
		return Snapshot{}, err
	}
	return result, nil
}

type snapshotWire struct {
	Version        string               `json:"version"`
	RunID          string               `json:"run_id"`
	Definition     snapshotDefinition   `json:"definition"`
	CompletedTurns uint32               `json:"completed_turns"`
	History        []snapshotMessage    `json:"history"`
	PlanIdentity   snapshotPlanIdentity `json:"plan_identity"`
	InteractionIDs []string             `json:"seen_interaction_ids"`
	LastSequence   uint64               `json:"last_sequence"`
	Status         LifecycleStatus      `json:"status"`
}

type snapshotPlanIdentity struct {
	CompiledIdentities            []string `json:"compiled_identities"`
	SnapshotCompatibilityIdentity string   `json:"snapshot_compatibility_identity"`
	WorkspaceFingerprint          string   `json:"workspace_fingerprint"`
	ToolPlanID                    string   `json:"tool_plan_id"`
	Fingerprint                   string   `json:"fingerprint"`
}

type snapshotDefinition struct {
	Name     string `json:"name"`
	Model    string `json:"model"`
	MaxTurns uint32 `json:"max_turns"`
}

type snapshotMessage struct {
	ID    string         `json:"id"`
	Role  message.Role   `json:"role"`
	Parts []snapshotPart `json:"parts"`
}

type snapshotPart struct {
	Kind      message.PartKind `json:"kind"`
	Text      string           `json:"text,omitempty"`
	Name      string           `json:"name,omitempty"`
	CallID    string           `json:"call_id,omitempty"`
	Namespace string           `json:"namespace,omitempty"`
	Data      json.RawMessage  `json:"data,omitempty"`
}

func (snapshot Snapshot) toWire() (snapshotWire, error) {
	history := make([]snapshotMessage, len(snapshot.history))
	for index, value := range snapshot.history {
		encoded, err := encodeSnapshotMessage(value)
		if err != nil {
			return snapshotWire{}, err
		}
		history[index] = encoded
	}
	return snapshotWire{
		Version: snapshot.version, RunID: snapshot.runID,
		Definition:     snapshotDefinition{Name: snapshot.definition.name, Model: snapshot.definition.model, MaxTurns: snapshot.definition.maxTurns},
		CompletedTurns: snapshot.completedTurns, History: history,
		PlanIdentity: snapshotPlanIdentity{
			CompiledIdentities:            snapshot.planIdentity.CompiledIdentities(),
			SnapshotCompatibilityIdentity: snapshot.planIdentity.SnapshotCompatibilityIdentity(),
			WorkspaceFingerprint:          snapshot.planIdentity.WorkspaceFingerprint(),
			ToolPlanID:                    snapshot.planIdentity.ToolPlanID().String(),
			Fingerprint:                   snapshot.planIdentity.Fingerprint(),
		},
		InteractionIDs: interactionIDStrings(snapshot.interactionIDs),
		LastSequence:   snapshot.lastSequence, Status: snapshot.status,
	}, nil
}

func encodeSnapshotMessage(value message.Message) (snapshotMessage, error) {
	if err := value.Validate(); err != nil {
		return snapshotMessage{}, err
	}
	parts := value.Parts()
	result := snapshotMessage{ID: string(value.ID()), Role: value.Role(), Parts: make([]snapshotPart, len(parts))}
	for index, part := range parts {
		text, _ := part.TextValue()
		result.Parts[index] = snapshotPart{
			Kind: part.Kind(), Text: text, Name: part.Name(), CallID: part.CallID(),
			Namespace: part.Namespace(), Data: part.Data(),
		}
	}
	return result, nil
}

func decodeSnapshotMessage(value snapshotMessage) (message.Message, error) {
	id, err := message.NewID(value.ID)
	if err != nil {
		return message.Message{}, err
	}
	parts := make([]message.Part, len(value.Parts))
	for index, part := range value.Parts {
		switch part.Kind {
		case message.PartText:
			if part.Name != "" || part.CallID != "" || part.Namespace != "" || len(part.Data) != 0 {
				return message.Message{}, errors.New("snapshot text part contains inactive fields")
			}
			parts[index], err = message.Text(part.Text)
		case message.PartToolCall:
			if part.Text != "" || part.Namespace != "" {
				return message.Message{}, errors.New("snapshot tool call contains inactive fields")
			}
			parts[index], err = message.ToolCall(part.CallID, part.Name, part.Data)
		case message.PartToolResult:
			if part.Text != "" || part.Namespace != "" {
				return message.Message{}, errors.New("snapshot tool result contains inactive fields")
			}
			parts[index], err = message.ToolResult(part.CallID, part.Name, part.Data)
		case message.PartExtension:
			if part.Text != "" || part.Name != "" || part.CallID != "" {
				return message.Message{}, errors.New("snapshot extension contains inactive fields")
			}
			parts[index], err = message.Extension(part.Namespace, part.Data)
		default:
			err = fmt.Errorf("snapshot message part kind %q is unsupported", part.Kind)
		}
		if err != nil {
			return message.Message{}, err
		}
	}
	return message.New(id, value.Role, parts...)
}

func validateSnapshotHistory(history []message.Message) error {
	if history[0].Role() != message.RoleUser {
		return errors.New("agent snapshot history must begin with the user input")
	}
	messageIDs := make(map[message.ID]struct{}, len(history))
	pending := make(map[string]string)
	seenCalls := make(map[string]struct{})
	total := 0
	for index, value := range history {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("agent snapshot message %d: %w", index, err)
		}
		if _, duplicate := messageIDs[value.ID()]; duplicate {
			return fmt.Errorf("agent snapshot message ID %q is duplicated", value.ID())
		}
		messageIDs[value.ID()] = struct{}{}
		total += value.SizeBytes()
		if total > MaximumSnapshotBytes {
			return fmt.Errorf("agent snapshot history exceeds %d bytes", MaximumSnapshotBytes)
		}
		for _, part := range value.Parts() {
			switch part.Kind() {
			case message.PartToolCall:
				if value.Role() != message.RoleAssistant {
					return errors.New("agent snapshot tool call must belong to an assistant message")
				}
				if _, duplicate := seenCalls[part.CallID()]; duplicate {
					return fmt.Errorf("agent snapshot tool call ID %q is duplicated", part.CallID())
				}
				seenCalls[part.CallID()] = struct{}{}
				pending[part.CallID()] = part.Name()
			case message.PartToolResult:
				if value.Role() != message.RoleTool {
					return errors.New("agent snapshot tool result must belong to a tool message")
				}
				name, found := pending[part.CallID()]
				if !found || name != part.Name() {
					return fmt.Errorf("agent snapshot tool result %q has no matching call", part.CallID())
				}
				delete(pending, part.CallID())
			}
		}
	}
	if len(pending) != 0 {
		return errors.New("agent snapshot contains uncertain tool calls without results")
	}
	return nil
}

func validateInteractionIDs(values []interaction.ID) error {
	if len(values) > maximumInteractionIDs {
		return fmt.Errorf("agent snapshot interaction ID count exceeds %d", maximumInteractionIDs)
	}
	for index, value := range values {
		if err := snapshotToken("interaction ID", string(value), 128); err != nil {
			return err
		}
		if index > 0 && values[index-1] >= value {
			return errors.New("agent snapshot interaction IDs must be sorted and unique")
		}
	}
	return nil
}

func interactionIDStrings(values []interaction.ID) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func interactionIDSet(values []interaction.ID) map[interaction.ID]struct{} {
	result := make(map[interaction.ID]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func snapshotMessageIDs(history []message.Message) map[message.ID]struct{} {
	result := make(map[message.ID]struct{}, len(history))
	for _, value := range history {
		result[value.ID()] = struct{}{}
	}
	return result
}

func cloneHistory(values []message.Message) []message.Message {
	result := make([]message.Message, len(values))
	for index, value := range values {
		result[index] = value.Clone()
	}
	return result
}

func snapshotToken(label, value string, maximum int) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("agent snapshot %s must be non-empty without surrounding whitespace", label)
	}
	if len(value) > maximum {
		return fmt.Errorf("agent snapshot %s exceeds %d bytes", label, maximum)
	}
	return nil
}

func ensureSnapshotEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("agent snapshot contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing agent snapshot data: %w", err)
	}
	return nil
}
