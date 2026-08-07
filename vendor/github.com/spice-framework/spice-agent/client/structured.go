package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
)

// MaximumStructuredValueBytes bounds interaction schemas and responses.
const MaximumStructuredValueBytes = 512 << 10

// StructuredKind identifies the top-level JSON value without exposing a raw
// protocol or map representation.
type StructuredKind string

const (
	StructuredNull   StructuredKind = "null"
	StructuredBool   StructuredKind = "boolean"
	StructuredNumber StructuredKind = "number"
	StructuredText   StructuredKind = "text"
	StructuredArray  StructuredKind = "array"
	StructuredObject StructuredKind = "object"
)

// StructuredValue is immutable bounded JSON used for portable interaction
// schemas and responses. It may contain secret-bearing application data and
// must never be copied into logs, events, status messages, or diagnostics.
// Default formatting, structured logging, and JSON encoding are redacted.
type StructuredValue struct {
	kind    StructuredKind
	encoded []byte
}

// ParseStructuredValue validates and defensively copies one arbitrary JSON
// value. Empty input is invalid; the JSON literal null is valid.
func ParseStructuredValue(encoded []byte) (StructuredValue, error) {
	if len(encoded) == 0 || len(encoded) > MaximumStructuredValueBytes {
		return StructuredValue{}, fmt.Errorf(
			"structured value must be between 1 and %d bytes",
			MaximumStructuredValueBytes,
		)
	}
	if !json.Valid(encoded) {
		return StructuredValue{}, errors.New("structured value must contain valid JSON")
	}
	trimmed := bytes.TrimSpace(encoded)
	kind, err := structuredKind(trimmed)
	if err != nil {
		return StructuredValue{}, err
	}
	return StructuredValue{kind: kind, encoded: slices.Clone(encoded)}, nil
}

// NewStructuredText constructs a JSON string value.
func NewStructuredText(value string) (StructuredValue, error) {
	if err := boundedText("structured text", value, MaximumStructuredValueBytes, true); err != nil {
		return StructuredValue{}, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return StructuredValue{}, fmt.Errorf("encode structured text: %w", err)
	}
	return ParseStructuredValue(encoded)
}

// NewStructuredBool constructs a JSON Boolean value.
func NewStructuredBool(value bool) StructuredValue {
	if value {
		return StructuredValue{kind: StructuredBool, encoded: []byte("true")}
	}
	return StructuredValue{kind: StructuredBool, encoded: []byte("false")}
}

// NewStructuredNull constructs the valid JSON null value.
func NewStructuredNull() StructuredValue {
	return StructuredValue{kind: StructuredNull, encoded: []byte("null")}
}

func (value StructuredValue) Kind() StructuredKind { return value.kind }

// EncodeTransfer returns a defensive copy of the exact validated, potentially
// sensitive JSON for an explicit adapter transfer. Callers must not use it for
// logs, events, errors, status, or diagnostics.
func (value StructuredValue) EncodeTransfer() ([]byte, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return slices.Clone(value.encoded), nil
}

// String redacts application data under ordinary formatting.
func (value StructuredValue) String() string { return "<structured-value:redacted>" }

// GoString redacts application data under Go-syntax formatting.
func (value StructuredValue) GoString() string { return value.String() }

// LogValue redacts application data under structured logging.
func (value StructuredValue) LogValue() slog.Value { return slog.StringValue(value.String()) }

// Text returns the decoded string when this is a JSON string.
func (value StructuredValue) Text() (string, bool) {
	if value.kind != StructuredText {
		return "", false
	}
	var result string
	if err := json.Unmarshal(value.encoded, &result); err != nil {
		return "", false
	}
	return result, true
}

// Bool returns the decoded Boolean when this is a JSON Boolean.
func (value StructuredValue) Bool() (bool, bool) {
	if value.kind != StructuredBool {
		return false, false
	}
	return bytes.Equal(bytes.TrimSpace(value.encoded), []byte("true")), true
}

func (value StructuredValue) IsNull() bool { return value.kind == StructuredNull }

func (value StructuredValue) Validate() error {
	reconstructed, err := ParseStructuredValue(value.encoded)
	if err != nil {
		return err
	}
	if reconstructed.kind != value.kind {
		return errors.New("structured value kind does not match its JSON")
	}
	return nil
}

func structuredKind(value []byte) (StructuredKind, error) {
	if len(value) == 0 {
		return "", errors.New("structured value is empty")
	}
	switch value[0] {
	case 'n':
		return StructuredNull, nil
	case 't', 'f':
		return StructuredBool, nil
	case '"':
		return StructuredText, nil
	case '[':
		return StructuredArray, nil
	case '{':
		return StructuredObject, nil
	default:
		return StructuredNumber, nil
	}
}
