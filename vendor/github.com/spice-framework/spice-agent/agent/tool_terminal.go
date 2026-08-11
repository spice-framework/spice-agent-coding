package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/spice-framework/spice-agent/event"
	"github.com/spice-framework/spice-agent/tool"
)

const (
	// ToolTerminalOccurrenceVersion identifies the durable agent-owned payload.
	ToolTerminalOccurrenceVersion = "spice.agent.tool-terminal/v1alpha1"
	// MaximumToolTerminalOccurrenceBytes bounds one terminal occurrence. Tool
	// output, problem text, paths, and provider data are deliberately excluded.
	MaximumToolTerminalOccurrenceBytes = 1024
)

// ToolTerminalOccurrence closes one ToolStarted occurrence with bounded,
// non-secret correlation and execution-safety facts.
type ToolTerminalOccurrence struct {
	callID  tool.CallID
	name    string
	kind    event.Kind
	outcome tool.ExecutionState
	retry   tool.RetryDisposition
}

// NewToolTerminalOccurrence constructs a terminal occurrence. Completed
// occurrences carry no execution-failure metadata. Failed occurrences may
// carry a complete validated state/retry pair or neither value.
func NewToolTerminalOccurrence(
	kind event.Kind,
	callID tool.CallID,
	name string,
	outcome tool.ExecutionState,
	retry tool.RetryDisposition,
) (ToolTerminalOccurrence, error) {
	result := ToolTerminalOccurrence{
		callID:  callID,
		name:    name,
		kind:    kind,
		outcome: outcome,
		retry:   retry,
	}
	if err := result.validate(); err != nil {
		return ToolTerminalOccurrence{}, err
	}
	return result, nil
}

// Encode returns canonical JSON suitable for one tool terminal event payload.
func (occurrence ToolTerminalOccurrence) Encode() (json.RawMessage, error) {
	if err := occurrence.validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(toolTerminalOccurrenceWire{
		Version: ToolTerminalOccurrenceVersion,
		CallID:  string(occurrence.callID),
		Name:    occurrence.name,
		Kind:    occurrence.kind,
		Outcome: occurrence.outcome,
		Retry:   occurrence.retry,
	})
	if err != nil || len(encoded) > MaximumToolTerminalOccurrenceBytes {
		return nil, errors.New("encode tool terminal occurrence failed")
	}
	return encoded, nil
}

// DecodeToolTerminalOccurrence validates an untrusted terminal payload against
// its owning event kind with exact fields, bounded size, and no duplicates.
func DecodeToolTerminalOccurrence(
	kind event.Kind,
	encoded json.RawMessage,
) (ToolTerminalOccurrence, error) {
	if !isToolTerminalKind(kind) {
		return ToolTerminalOccurrence{}, errors.New("tool terminal occurrence event kind is unsupported")
	}
	if len(encoded) == 0 || len(encoded) > MaximumToolTerminalOccurrenceBytes {
		return ToolTerminalOccurrence{}, errors.New("tool terminal occurrence size is invalid")
	}
	wire, err := decodeToolTerminalWire(encoded)
	if err != nil {
		return ToolTerminalOccurrence{}, err
	}
	if event.Kind(*wire.Kind) != kind {
		return ToolTerminalOccurrence{}, errors.New("tool terminal occurrence kind does not match its event")
	}
	result := ToolTerminalOccurrence{
		callID:  tool.CallID(*wire.CallID),
		name:    *wire.Name,
		kind:    kind,
		outcome: tool.ExecutionState(*wire.Outcome),
		retry:   tool.RetryDisposition(*wire.Retry),
	}
	if err = result.validate(); err != nil {
		return ToolTerminalOccurrence{}, errors.New("tool terminal occurrence fields are invalid")
	}
	return result, nil
}

func (occurrence ToolTerminalOccurrence) validate() error {
	if _, err := tool.NewCall(occurrence.callID, occurrence.name, json.RawMessage(`{}`)); err != nil {
		return errors.New("tool terminal occurrence call identity is invalid")
	}
	if !isToolTerminalKind(occurrence.kind) {
		return errors.New("tool terminal occurrence event kind is unsupported")
	}
	if occurrence.kind == event.ToolCompleted {
		if occurrence.outcome != "" || occurrence.retry != "" {
			return errors.New("completed tool occurrence contains failure metadata")
		}
		return nil
	}
	if occurrence.outcome == "" && occurrence.retry == "" {
		return nil
	}
	if occurrence.outcome == "" || occurrence.retry == "" {
		return errors.New("failed tool occurrence has incomplete execution metadata")
	}
	if _, err := tool.NewExecutionError(
		occurrence.callID,
		occurrence.outcome,
		occurrence.retry,
		errors.New("tool terminal occurrence validation"),
	); err != nil {
		return errors.New("failed tool occurrence execution metadata is invalid")
	}
	return nil
}

func isToolTerminalKind(kind event.Kind) bool {
	return kind == event.ToolCompleted || kind == event.ToolFailed
}

func decodeToolTerminalWire(encoded json.RawMessage) (toolTerminalOccurrenceDecodeWire, error) {
	if err := validateToolTerminalObject(encoded); err != nil {
		return toolTerminalOccurrenceDecodeWire{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var wire toolTerminalOccurrenceDecodeWire
	if err := decoder.Decode(&wire); err != nil {
		return toolTerminalOccurrenceDecodeWire{}, errors.New("tool terminal occurrence JSON is invalid")
	}
	if err := requireToolTerminalEOF(decoder); err != nil {
		return toolTerminalOccurrenceDecodeWire{}, err
	}
	if !wire.complete() {
		return toolTerminalOccurrenceDecodeWire{}, errors.New("tool terminal occurrence is missing a required field")
	}
	if *wire.Version != ToolTerminalOccurrenceVersion {
		return toolTerminalOccurrenceDecodeWire{}, errors.New("tool terminal occurrence version is unsupported")
	}
	return wire, nil
}

func validateToolTerminalObject(encoded json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	token, err := decoder.Token()
	opening, ok := token.(json.Delim)
	if err != nil || !ok || opening != '{' {
		return errors.New("tool terminal occurrence JSON is invalid")
	}
	seen := make(map[string]struct{}, 6)
	for decoder.More() {
		keyToken, tokenErr := decoder.Token()
		key, keyOK := keyToken.(string)
		if tokenErr != nil || !keyOK {
			return errors.New("tool terminal occurrence JSON is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("tool terminal occurrence contains a duplicate field")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if decodeErr := decoder.Decode(&value); decodeErr != nil {
			return errors.New("tool terminal occurrence JSON is invalid")
		}
	}
	closing, err := decoder.Token()
	if delimiter, closingOK := closing.(json.Delim); err != nil || !closingOK || delimiter != '}' {
		return errors.New("tool terminal occurrence JSON is invalid")
	}
	return requireToolTerminalEOF(decoder)
}

func requireToolTerminalEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("tool terminal occurrence JSON has trailing data")
	}
	return nil
}

// CallID returns the model call correlation identity.
func (occurrence ToolTerminalOccurrence) CallID() tool.CallID { return occurrence.callID }

// Name returns the canonical requested tool name.
func (occurrence ToolTerminalOccurrence) Name() string { return occurrence.name }

// Kind returns ToolCompleted or ToolFailed.
func (occurrence ToolTerminalOccurrence) Kind() event.Kind { return occurrence.kind }

// ExecutionState returns safe typed failure state when the dispatcher supplied it.
func (occurrence ToolTerminalOccurrence) ExecutionState() tool.ExecutionState {
	return occurrence.outcome
}

// RetryDisposition returns safe typed retry advice when the dispatcher supplied it.
func (occurrence ToolTerminalOccurrence) RetryDisposition() tool.RetryDisposition {
	return occurrence.retry
}

type toolTerminalOccurrenceWire struct {
	Version string                `json:"version"`
	CallID  string                `json:"call_id"`
	Name    string                `json:"name"`
	Kind    event.Kind            `json:"kind"`
	Outcome tool.ExecutionState   `json:"outcome"`
	Retry   tool.RetryDisposition `json:"retry"`
}

type toolTerminalOccurrenceDecodeWire struct {
	Version *string `json:"version"`
	CallID  *string `json:"call_id"`
	Name    *string `json:"name"`
	Kind    *string `json:"kind"`
	Outcome *string `json:"outcome"`
	Retry   *string `json:"retry"`
}

func (wire toolTerminalOccurrenceDecodeWire) complete() bool {
	return wire.Version != nil && wire.CallID != nil && wire.Name != nil && wire.Kind != nil &&
		wire.Outcome != nil && wire.Retry != nil
}
