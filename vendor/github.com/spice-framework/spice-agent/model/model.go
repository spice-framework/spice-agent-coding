package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spice-framework/spice-agent/message"
	"github.com/spice-framework/spice-agent/tool"
)

const (
	maxOperationIDBytes   = 128
	maxModelNameBytes     = 256
	maxRequestMessages    = 4096
	maxRequestTools       = 512
	maxRequestBytes       = 32 << 20
	maxMetadataItems      = 32
	maxMetadataItemBytes  = 64 << 10
	maxMetadataTotalBytes = 256 << 10
)

const (
	// MaximumTextDeltaBytes bounds one provider stream text item.
	MaximumTextDeltaBytes = 256 << 10
	// MaximumOperationTextBytes bounds all text observed in one operation.
	MaximumOperationTextBytes = 4 << 20
	// MaximumOperationToolCalls bounds all calls observed in one operation.
	MaximumOperationToolCalls = 128
)

// Metadata is one provider-neutral, namespaced, immutable JSON value. Provider
// adapters may expose safe response identity and service metadata, but must
// never include credentials, authorization values, prompts, or other secrets.
type Metadata struct {
	namespace string
	value     json.RawMessage
}

// NewMetadata validates and defensively copies one namespaced JSON value.
func NewMetadata(namespace string, value json.RawMessage) (Metadata, error) {
	if err := validateToken("model metadata namespace", namespace, 128); err != nil {
		return Metadata{}, err
	}
	if len(value) == 0 || len(value) > maxMetadataItemBytes {
		return Metadata{}, fmt.Errorf("model metadata value must be between 1 and %d bytes", maxMetadataItemBytes)
	}
	if !json.Valid(value) {
		return Metadata{}, errors.New("model metadata value must be valid JSON")
	}
	return Metadata{namespace: namespace, value: append(json.RawMessage(nil), value...)}, nil
}

func (metadata Metadata) Validate() error {
	_, err := NewMetadata(metadata.namespace, metadata.value)
	return err
}

func (metadata Metadata) Namespace() string { return metadata.namespace }

func (metadata Metadata) Value() json.RawMessage {
	return append(json.RawMessage(nil), metadata.value...)
}

func (metadata Metadata) Clone() Metadata {
	return Metadata{namespace: metadata.namespace, value: metadata.Value()}
}

func cloneMetadata(values []Metadata) ([]Metadata, error) {
	if len(values) > maxMetadataItems {
		return nil, fmt.Errorf("model metadata count exceeds %d", maxMetadataItems)
	}
	result := make([]Metadata, len(values))
	total := 0
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("model metadata %d: %w", index, err)
		}
		if _, duplicate := seen[value.namespace]; duplicate {
			return nil, fmt.Errorf("model metadata namespace %q is duplicated", value.namespace)
		}
		seen[value.namespace] = struct{}{}
		total += len(value.namespace) + len(value.value)
		if total > maxMetadataTotalBytes {
			return nil, fmt.Errorf("model metadata exceeds %d bytes", maxMetadataTotalBytes)
		}
		result[index] = value.Clone()
	}
	return result, nil
}

// OperationID identifies one provider request for tracing and idempotency.
type OperationID string

// Request is an immutable snapshot of one model operation.
type Request struct {
	operationID OperationID
	model       string
	messages    []message.Message
	tools       []tool.Definition
}

// NewRequest validates and copies one provider request.
func NewRequest(operationID OperationID, modelName string, messages []message.Message, tools []tool.Definition) (Request, error) {
	if err := validateToken("model operation ID", string(operationID), maxOperationIDBytes); err != nil {
		return Request{}, err
	}
	if err := validateToken("model name", modelName, maxModelNameBytes); err != nil {
		return Request{}, err
	}
	if len(messages) == 0 || len(messages) > maxRequestMessages {
		return Request{}, fmt.Errorf("model request message count must be between 1 and %d", maxRequestMessages)
	}
	if len(tools) > maxRequestTools {
		return Request{}, fmt.Errorf("model request tool count exceeds %d", maxRequestTools)
	}
	result := Request{
		operationID: operationID,
		model:       modelName,
		messages:    make([]message.Message, len(messages)),
		tools:       make([]tool.Definition, len(tools)),
	}
	totalBytes := len(operationID) + len(modelName)
	for index, value := range messages {
		if err := value.Validate(); err != nil {
			return Request{}, fmt.Errorf("model message %d: %w", index, err)
		}
		result.messages[index] = value.Clone()
		totalBytes += value.SizeBytes()
		if totalBytes > maxRequestBytes {
			return Request{}, fmt.Errorf("model request exceeds %d bytes", maxRequestBytes)
		}
	}
	seen := make(map[string]struct{}, len(tools))
	for index, definition := range tools {
		if err := definition.Validate(); err != nil {
			return Request{}, fmt.Errorf("model tool %d: %w", index, err)
		}
		if _, duplicate := seen[definition.Name()]; duplicate {
			return Request{}, fmt.Errorf("model tool %q is duplicated", definition.Name())
		}
		seen[definition.Name()] = struct{}{}
		result.tools[index] = definition.Clone()
		totalBytes += definition.SizeBytes()
		if totalBytes > maxRequestBytes {
			return Request{}, fmt.Errorf("model request exceeds %d bytes", maxRequestBytes)
		}
	}
	return result, nil
}

// OperationID returns the provider operation identity.
func (request Request) OperationID() OperationID { return request.operationID }

// Model returns the selected provider model name. It is the authoritative model
// selection; provider configuration owns transport defaults, not model choice.
func (request Request) Model() string { return request.model }

// Messages returns a defensive copy of immutable messages.
func (request Request) Messages() []message.Message {
	result := make([]message.Message, len(request.messages))
	for index, value := range request.messages {
		result[index] = value.Clone()
	}
	return result
}

// Tools returns deep defensive copies.
func (request Request) Tools() []tool.Definition {
	result := make([]tool.Definition, len(request.tools))
	for index, definition := range request.tools {
		result[index] = definition.Clone()
	}
	return result
}

// EventKind identifies a model stream item.
type EventKind string

const (
	EventTextDelta EventKind = "text_delta"
	EventToolCall  EventKind = "tool_call"
	EventCompleted EventKind = "completed"
	EventFailed    EventKind = "failed"
)

// Usage contains provider-normalized token accounting. Zero means unknown.
type Usage struct {
	inputTokens  uint64
	outputTokens uint64
}

// NewUsage constructs token accounting.
func NewUsage(inputTokens, outputTokens uint64) Usage {
	return Usage{inputTokens: inputTokens, outputTokens: outputTokens}
}

// InputTokens returns provider input-token usage.
func (usage Usage) InputTokens() uint64 { return usage.inputTokens }

// OutputTokens returns provider output-token usage.
func (usage Usage) OutputTokens() uint64 { return usage.outputTokens }

// TotalTokens returns the overflow-safe sum, saturating at uint64 maximum.
func (usage Usage) TotalTokens() uint64 {
	total := usage.inputTokens + usage.outputTokens
	if total < usage.inputTokens {
		return ^uint64(0)
	}
	return total
}

// Problem is immutable provider-neutral typed failure metadata.
type Problem struct {
	code      string
	message   string
	retryable bool
	metadata  []Metadata
}

// Validate rejects a zero or corrupted problem.
func (problem Problem) Validate() error {
	_, err := NewProblem(problem.code, problem.message, problem.retryable, problem.metadata...)
	return err
}

// NewProblem validates one provider failure.
func NewProblem(code, problemMessage string, retryable bool, metadata ...Metadata) (Problem, error) {
	if err := validateToken("model problem code", code, 128); err != nil {
		return Problem{}, err
	}
	if problemMessage == "" || problemMessage != strings.TrimSpace(problemMessage) {
		return Problem{}, errors.New("model problem message must be non-empty without surrounding whitespace")
	}
	if len(problemMessage) > 4096 {
		return Problem{}, errors.New("model problem message exceeds 4096 bytes")
	}
	metadataCopy, err := cloneMetadata(metadata)
	if err != nil {
		return Problem{}, err
	}
	return Problem{code: code, message: problemMessage, retryable: retryable, metadata: metadataCopy}, nil
}

// Code returns the stable failure category.
func (problem Problem) Code() string { return problem.code }

// Message returns safe provider-neutral detail.
func (problem Problem) Message() string { return problem.message }

// Retryable reports provider-declared retry safety. The host additionally
// requires that no stream item was observed.
func (problem Problem) Retryable() bool { return problem.retryable }

// Metadata returns defensive provider extension metadata.
func (problem Problem) Metadata() []Metadata {
	result, _ := cloneMetadata(problem.metadata)
	return result
}

// ProviderError preserves a typed failure returned before a stream exists.
type ProviderError struct {
	problem Problem
	cause   error
}

// StreamError preserves a typed provider failure returned by Stream.Recv.
// Retry position is host-observed and is not supplied by this value.
type StreamError struct {
	problem Problem
	cause   error
}

// NewStreamError constructs a typed provider stream failure.
func NewStreamError(problem Problem, cause error) (*StreamError, error) {
	if err := problem.Validate(); err != nil {
		return nil, err
	}
	return &StreamError{problem: problem, cause: cause}, nil
}

func (failure *StreamError) Error() string {
	if failure == nil {
		return "model stream failure"
	}
	return failure.problem.code + ": " + failure.problem.message
}

func (failure *StreamError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

// Problem returns immutable typed metadata.
func (failure *StreamError) Problem() Problem {
	if failure == nil {
		return Problem{}
	}
	return failure.problem
}

// NewProviderError constructs a typed provider-start failure.
func NewProviderError(problem Problem, cause error) (*ProviderError, error) {
	if err := problem.Validate(); err != nil {
		return nil, err
	}
	return &ProviderError{problem: problem, cause: cause}, nil
}

// Error implements error.
func (failure *ProviderError) Error() string {
	if failure == nil {
		return "model provider failure"
	}
	return failure.problem.code + ": " + failure.problem.message
}

// Unwrap returns the provider-owned cause, which must not contain secrets.
func (failure *ProviderError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

// Problem returns immutable typed metadata.
func (failure *ProviderError) Problem() Problem {
	if failure == nil {
		return Problem{}
	}
	return failure.problem
}

// OperationError is a host-observed model failure. BeforeStream is computed by
// the engine and cannot be asserted by a provider.
type OperationError struct {
	problem  Problem
	observed bool
	cause    error
}

// NewOperationError records whether any stream item was already observable.
func NewOperationError(problem Problem, observed bool, cause error) (*OperationError, error) {
	if err := problem.Validate(); err != nil {
		return nil, err
	}
	return &OperationError{problem: problem, observed: observed, cause: cause}, nil
}

func (failure *OperationError) Error() string {
	if failure == nil {
		return "model operation failure"
	}
	return failure.problem.code + ": " + failure.problem.message
}

func (failure *OperationError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

// Problem returns typed metadata.
func (failure *OperationError) Problem() Problem {
	if failure == nil {
		return Problem{}
	}
	return failure.problem
}

// BeforeStream reports host-observed retry position.
func (failure *OperationError) BeforeStream() bool { return failure != nil && !failure.observed }

// Retryable reports safe retry only when the provider permits it and no stream
// item was observed by the host.
func (failure *OperationError) Retryable() bool {
	return failure != nil && failure.problem.retryable && !failure.observed
}

// StreamEvent is an immutable strict tagged union.
type StreamEvent struct {
	kind     EventKind
	text     string
	call     tool.Call
	usage    Usage
	problem  Problem
	metadata []Metadata
}

// TextDelta constructs one bounded text event.
func TextDelta(text string) (StreamEvent, error) {
	if text == "" {
		return StreamEvent{}, errors.New("model text delta must not be empty")
	}
	if len(text) > MaximumTextDeltaBytes {
		return StreamEvent{}, fmt.Errorf("model text delta exceeds %d bytes", MaximumTextDeltaBytes)
	}
	return StreamEvent{kind: EventTextDelta, text: text}, nil
}

// ToolCallEvent constructs one tool-call event.
func ToolCallEvent(call tool.Call) (StreamEvent, error) {
	if err := call.Validate(); err != nil {
		return StreamEvent{}, err
	}
	return StreamEvent{kind: EventToolCall, call: call.Clone()}, nil
}

// Completed constructs one terminal success event.
func Completed(usage Usage, metadata ...Metadata) (StreamEvent, error) {
	metadataCopy, err := cloneMetadata(metadata)
	if err != nil {
		return StreamEvent{}, err
	}
	return StreamEvent{kind: EventCompleted, usage: usage, metadata: metadataCopy}, nil
}

// Failed constructs one terminal failure event.
func Failed(problem Problem) (StreamEvent, error) {
	if err := problem.Validate(); err != nil {
		return StreamEvent{}, err
	}
	return StreamEvent{kind: EventFailed, problem: problem}, nil
}

// Kind returns the discriminator.
func (streamEvent StreamEvent) Kind() EventKind { return streamEvent.kind }

// Text returns text and whether the event is a text delta.
func (streamEvent StreamEvent) Text() (string, bool) {
	return streamEvent.text, streamEvent.kind == EventTextDelta
}

// Call returns a defensive call and whether the event is a tool call.
func (streamEvent StreamEvent) Call() (tool.Call, bool) {
	return streamEvent.call.Clone(), streamEvent.kind == EventToolCall
}

// Usage returns accounting and whether the event completed successfully.
func (streamEvent StreamEvent) Usage() (Usage, bool) {
	return streamEvent.usage, streamEvent.kind == EventCompleted
}

// Problem returns failure metadata and whether the event failed.
func (streamEvent StreamEvent) Problem() (Problem, bool) {
	problem, err := NewProblem(streamEvent.problem.code, streamEvent.problem.message, streamEvent.problem.retryable, streamEvent.problem.metadata...)
	if err != nil {
		return Problem{}, false
	}
	return problem, streamEvent.kind == EventFailed
}

// Metadata returns success metadata for completed events. Failed-event
// metadata is carried by Problem.Metadata.
func (streamEvent StreamEvent) Metadata() ([]Metadata, bool) {
	if streamEvent.kind != EventCompleted {
		return nil, false
	}
	result, err := cloneMetadata(streamEvent.metadata)
	return result, err == nil
}

// Validate reconstructs the active union member and rejects zero/corruption.
func (streamEvent StreamEvent) Validate() error {
	switch streamEvent.kind {
	case EventTextDelta:
		_, err := TextDelta(streamEvent.text)
		if err != nil {
			return err
		}
		return streamEvent.requireInactive(false, true, true, true)
	case EventToolCall:
		if _, err := ToolCallEvent(streamEvent.call); err != nil {
			return err
		}
		return streamEvent.requireInactive(true, false, true, true)
	case EventCompleted:
		if _, err := cloneMetadata(streamEvent.metadata); err != nil {
			return err
		}
		return streamEvent.requireInactive(true, true, false, false)
	case EventFailed:
		if _, err := Failed(streamEvent.problem); err != nil {
			return err
		}
		return streamEvent.requireInactive(true, true, true, true)
	default:
		return fmt.Errorf("model stream event kind %q is unsupported", streamEvent.kind)
	}
}

func (streamEvent StreamEvent) requireInactive(text, call, usage, metadata bool) error {
	if text && streamEvent.text != "" {
		return errors.New("model stream event contains inactive text payload")
	}
	if call && streamEvent.call.ID() != "" {
		return errors.New("model stream event contains inactive tool-call payload")
	}
	if usage && (streamEvent.usage.inputTokens != 0 || streamEvent.usage.outputTokens != 0) {
		return errors.New("model stream event contains inactive usage payload")
	}
	if streamEvent.kind != EventFailed && streamEvent.problem.code != "" {
		return errors.New("model stream event contains inactive problem payload")
	}
	if metadata && len(streamEvent.metadata) != 0 {
		return errors.New("model stream event contains inactive metadata payload")
	}
	return nil
}

// Stream supplies ordered model events. Recv returns io.EOF only after a valid
// completed or failed event has already been returned.
type Stream interface {
	Recv(context.Context) (StreamEvent, error)
	Close() error
}

// Provider starts one model operation. Implementations must be safe for
// concurrent calls and must not retry after any stream item is observable.
// Cancellation is cooperative for trusted in-process implementations.
type Provider interface {
	Stream(context.Context, Request) (Stream, error)
}

// RequireCompletion converts premature EOF into a contract error.
func RequireCompletion(err error, terminal bool) error {
	if !errors.Is(err, io.EOF) || terminal {
		return err
	}
	return errors.New("model stream ended before a terminal event")
}

func validateToken(label, value string, maximum int) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be non-empty without surrounding whitespace", label)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	return nil
}
