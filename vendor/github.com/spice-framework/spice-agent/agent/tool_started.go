package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
)

const (
	// ToolStartedOccurrenceVersion identifies the durable agent-owned payload.
	ToolStartedOccurrenceVersion = "spice.agent.tool-started/v1alpha1"
	// MaximumToolStartedOccurrenceBytes is the independent decode bound for one
	// tool-start occurrence. Arguments, paths, and provider data are excluded.
	MaximumToolStartedOccurrenceBytes = 4096
)

// ToolStartedOccurrence contains immutable, non-secret dispatch facts recorded
// before one model-requested tool call is admitted to canonical dispatch.
type ToolStartedOccurrence struct {
	callID                tool.CallID
	name                  string
	declared              bool
	executable            bool
	definitionFingerprint string
	effect                tool.Effect
	replaySafety          tool.ReplaySafety
	capabilities          []tool.Capability
	toolPlanID            stage.PlanID
	planFingerprint       string
	workspaceFingerprint  string
	turn                  uint32
}

// NewToolStartedOccurrence constructs durable tool-start facts without
// accepting arguments or any other executable input. Definition is required
// when the name was declared and must be nil for an unknown model tool.
func NewToolStartedOccurrence(
	callID tool.CallID,
	name string,
	declared bool,
	executable bool,
	definition *tool.Definition,
	planIdentity PlanIdentity,
	turn uint32,
) (ToolStartedOccurrence, error) {
	if _, err := tool.NewCall(callID, name, json.RawMessage(`{}`)); err != nil {
		return ToolStartedOccurrence{}, errors.New("tool started occurrence call identity is invalid")
	}
	if err := planIdentity.Validate(); err != nil {
		return ToolStartedOccurrence{}, errors.New("tool started occurrence plan identity is invalid")
	}
	result := ToolStartedOccurrence{
		callID: callID, name: name, declared: declared, executable: executable,
		toolPlanID: planIdentity.ToolPlanID(), planFingerprint: planIdentity.Fingerprint(),
		workspaceFingerprint: planIdentity.WorkspaceFingerprint(), turn: turn,
		capabilities: make([]tool.Capability, 0),
	}
	if definition != nil {
		snapshot := definition.Clone()
		if snapshot.Validate() != nil || snapshot.Name() != name {
			return ToolStartedOccurrence{}, errors.New("tool started occurrence definition is invalid")
		}
		capabilities := snapshot.Capabilities()
		result.definitionFingerprint = snapshot.Fingerprint()
		result.effect = snapshot.Effect()
		result.replaySafety = snapshot.ReplaySafety()
		result.capabilities = append(make([]tool.Capability, 0, len(capabilities)), capabilities...)
	}
	if err := result.validate(); err != nil {
		return ToolStartedOccurrence{}, err
	}
	return result, nil
}

// Encode returns canonical JSON suitable for an event payload.
func (occurrence ToolStartedOccurrence) Encode() (json.RawMessage, error) {
	if err := occurrence.validate(); err != nil {
		return nil, err
	}
	wire := toolStartedOccurrenceWire{
		Version: ToolStartedOccurrenceVersion,
		CallID:  string(occurrence.callID), Name: occurrence.name,
		Declared: occurrence.declared, Executable: occurrence.executable,
		DefinitionFingerprint: occurrence.definitionFingerprint,
		Effect:                occurrence.effect, ReplaySafety: occurrence.replaySafety,
		Capabilities: append(make([]tool.Capability, 0, len(occurrence.capabilities)), occurrence.capabilities...),
		ToolPlanID:   occurrence.toolPlanID.String(), PlanFingerprint: occurrence.planFingerprint,
		WorkspaceFingerprint: occurrence.workspaceFingerprint, Turn: occurrence.turn,
	}
	encoded, err := json.Marshal(wire)
	if err != nil || len(encoded) > MaximumToolStartedOccurrenceBytes {
		return nil, errors.New("encode tool started occurrence failed")
	}
	return encoded, nil
}

// DecodeToolStartedOccurrence validates one untrusted durable payload with
// exact fields, bounded size, and no unknown or duplicate members.
func DecodeToolStartedOccurrence(encoded json.RawMessage) (ToolStartedOccurrence, error) {
	if len(encoded) == 0 || len(encoded) > MaximumToolStartedOccurrenceBytes {
		return ToolStartedOccurrence{}, errors.New("tool started occurrence size is invalid")
	}
	wire, err := decodeToolStartedWire(encoded)
	if err != nil {
		return ToolStartedOccurrence{}, err
	}
	planID, err := stage.NewPlanID(*wire.ToolPlanID)
	if err != nil {
		return ToolStartedOccurrence{}, errors.New("tool started occurrence fields are invalid")
	}
	result := ToolStartedOccurrence{
		callID: tool.CallID(*wire.CallID), name: *wire.Name,
		declared: *wire.Declared, executable: *wire.Executable,
		definitionFingerprint: *wire.DefinitionFingerprint,
		effect:                tool.Effect(*wire.Effect), replaySafety: tool.ReplaySafety(*wire.ReplaySafety),
		capabilities: append([]tool.Capability(nil), (*wire.Capabilities)...),
		toolPlanID:   planID, planFingerprint: *wire.PlanFingerprint,
		workspaceFingerprint: *wire.WorkspaceFingerprint, turn: *wire.Turn,
	}
	if err = result.validate(); err != nil {
		return ToolStartedOccurrence{}, errors.New("tool started occurrence fields are invalid")
	}
	return result, nil
}

func decodeToolStartedWire(encoded json.RawMessage) (toolStartedOccurrenceDecodeWire, error) {
	if err := validateToolStartedObject(encoded); err != nil {
		return toolStartedOccurrenceDecodeWire{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var wire toolStartedOccurrenceDecodeWire
	if err := decoder.Decode(&wire); err != nil {
		return toolStartedOccurrenceDecodeWire{}, errors.New("tool started occurrence JSON is invalid")
	}
	if err := requireToolStartedEOF(decoder); err != nil {
		return toolStartedOccurrenceDecodeWire{}, err
	}
	if !wire.complete() {
		return toolStartedOccurrenceDecodeWire{}, errors.New("tool started occurrence is missing a required field")
	}
	if *wire.Version != ToolStartedOccurrenceVersion {
		return toolStartedOccurrenceDecodeWire{}, errors.New("tool started occurrence version is unsupported")
	}
	return wire, nil
}

func (occurrence ToolStartedOccurrence) validate() error {
	if _, err := tool.NewCall(occurrence.callID, occurrence.name, json.RawMessage(`{}`)); err != nil {
		return errors.New("tool started occurrence call identity is invalid")
	}
	if occurrence.turn == 0 {
		return errors.New("tool started occurrence turn must be positive")
	}
	if err := occurrence.toolPlanID.Validate(); err != nil {
		return errors.New("tool started occurrence plan ID is invalid")
	}
	if !validOccurrenceFingerprint(occurrence.planFingerprint, true) ||
		(occurrence.workspaceFingerprint != "" && !validOccurrenceFingerprint(occurrence.workspaceFingerprint, true)) {
		return errors.New("tool started occurrence plan authority is invalid")
	}
	if occurrence.executable && !occurrence.declared {
		return errors.New("tool started occurrence cannot execute an undeclared tool")
	}
	if !occurrence.declared {
		if occurrence.definitionFingerprint != "" || occurrence.effect != "" || occurrence.replaySafety != "" || len(occurrence.capabilities) != 0 {
			return errors.New("unknown tool occurrence contains definition metadata")
		}
		return nil
	}
	if !validOccurrenceFingerprint(occurrence.definitionFingerprint, false) {
		return errors.New("tool started occurrence definition fingerprint is invalid")
	}
	if _, err := tool.NewDefinition(
		occurrence.name, "Tool occurrence validation.", json.RawMessage(`{}`),
		occurrence.effect, occurrence.replaySafety, occurrence.capabilities...,
	); err != nil {
		return errors.New("tool started occurrence definition metadata is invalid")
	}
	return nil
}

func validOccurrenceFingerprint(value string, prefixed bool) bool {
	if prefixed {
		if !strings.HasPrefix(value, planFingerprintPrefix) {
			return false
		}
		value = strings.TrimPrefix(value, planFingerprintPrefix)
	}
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateToolStartedObject(encoded json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	token, err := decoder.Token()
	opening, ok := token.(json.Delim)
	if err != nil || !ok || opening != '{' {
		return errors.New("tool started occurrence JSON is invalid")
	}
	seen := make(map[string]struct{}, 13)
	for decoder.More() {
		keyToken, tokenErr := decoder.Token()
		key, keyOK := keyToken.(string)
		if tokenErr != nil || !keyOK {
			return errors.New("tool started occurrence JSON is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("tool started occurrence contains a duplicate field")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if decodeErr := decoder.Decode(&value); decodeErr != nil {
			return errors.New("tool started occurrence JSON is invalid")
		}
	}
	closing, err := decoder.Token()
	if delimiter, closingOK := closing.(json.Delim); err != nil || !closingOK || delimiter != '}' {
		return errors.New("tool started occurrence JSON is invalid")
	}
	return requireToolStartedEOF(decoder)
}

func requireToolStartedEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("tool started occurrence JSON has trailing data")
	}
	return nil
}

// CallID returns the model call correlation identity.
func (occurrence ToolStartedOccurrence) CallID() tool.CallID { return occurrence.callID }

// Name returns the canonical requested tool name.
func (occurrence ToolStartedOccurrence) Name() string { return occurrence.name }

// Declared reports whether the leased plan exposed the tool to the model.
func (occurrence ToolStartedOccurrence) Declared() bool { return occurrence.declared }

// Executable reports whether canonical dispatch had an executable route.
func (occurrence ToolStartedOccurrence) Executable() bool { return occurrence.executable }

// DefinitionFingerprint returns the exact declared definition identity.
func (occurrence ToolStartedOccurrence) DefinitionFingerprint() string {
	return occurrence.definitionFingerprint
}

// Effect returns the declared external-state effect.
func (occurrence ToolStartedOccurrence) Effect() tool.Effect { return occurrence.effect }

// ReplaySafety returns the declared replay contract.
func (occurrence ToolStartedOccurrence) ReplaySafety() tool.ReplaySafety {
	return occurrence.replaySafety
}

// Capabilities returns a defensive copy of declared capabilities.
func (occurrence ToolStartedOccurrence) Capabilities() []tool.Capability {
	return append([]tool.Capability(nil), occurrence.capabilities...)
}

// ToolPlanID returns the exact leased generation identity.
func (occurrence ToolStartedOccurrence) ToolPlanID() stage.PlanID { return occurrence.toolPlanID }

// PlanFingerprint returns the combined immutable execution-plan identity.
func (occurrence ToolStartedOccurrence) PlanFingerprint() string { return occurrence.planFingerprint }

// WorkspaceFingerprint returns the workspace authority digest when portable.
func (occurrence ToolStartedOccurrence) WorkspaceFingerprint() string {
	return occurrence.workspaceFingerprint
}

// Turn returns the owning positive turn number.
func (occurrence ToolStartedOccurrence) Turn() uint32 { return occurrence.turn }

type toolStartedOccurrenceWire struct {
	Version               string            `json:"version"`
	CallID                string            `json:"call_id"`
	Name                  string            `json:"name"`
	Declared              bool              `json:"declared"`
	Executable            bool              `json:"executable"`
	DefinitionFingerprint string            `json:"definition_fingerprint"`
	Effect                tool.Effect       `json:"effect"`
	ReplaySafety          tool.ReplaySafety `json:"replay_safety"`
	Capabilities          []tool.Capability `json:"capabilities"`
	ToolPlanID            string            `json:"tool_plan_id"`
	PlanFingerprint       string            `json:"plan_fingerprint"`
	WorkspaceFingerprint  string            `json:"workspace_fingerprint"`
	Turn                  uint32            `json:"turn"`
}

type toolStartedOccurrenceDecodeWire struct {
	Version               *string            `json:"version"`
	CallID                *string            `json:"call_id"`
	Name                  *string            `json:"name"`
	Declared              *bool              `json:"declared"`
	Executable            *bool              `json:"executable"`
	DefinitionFingerprint *string            `json:"definition_fingerprint"`
	Effect                *string            `json:"effect"`
	ReplaySafety          *string            `json:"replay_safety"`
	Capabilities          *[]tool.Capability `json:"capabilities"`
	ToolPlanID            *string            `json:"tool_plan_id"`
	PlanFingerprint       *string            `json:"plan_fingerprint"`
	WorkspaceFingerprint  *string            `json:"workspace_fingerprint"`
	Turn                  *uint32            `json:"turn"`
}

func (wire toolStartedOccurrenceDecodeWire) complete() bool {
	return wire.Version != nil && wire.CallID != nil && wire.Name != nil && wire.Declared != nil && wire.Executable != nil &&
		wire.DefinitionFingerprint != nil && wire.Effect != nil && wire.ReplaySafety != nil && wire.Capabilities != nil &&
		wire.ToolPlanID != nil && wire.PlanFingerprint != nil && wire.WorkspaceFingerprint != nil && wire.Turn != nil
}
