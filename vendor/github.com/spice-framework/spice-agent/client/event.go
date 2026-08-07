package client

import (
	"errors"
	"fmt"
	"time"
)

// EventKind identifies one stable provider-neutral run occurrence.
type EventKind string

const (
	EventRunStarted           EventKind = "run.started"
	EventRunCompleted         EventKind = "run.completed"
	EventRunFailed            EventKind = "run.failed"
	EventRunCancelled         EventKind = "run.cancelled"
	EventTurnStarted          EventKind = "turn.started"
	EventTurnCompleted        EventKind = "turn.completed"
	EventTurnFailed           EventKind = "turn.failed"
	EventModelStarted         EventKind = "model.started"
	EventModelDelta           EventKind = "model.delta"
	EventModelCompleted       EventKind = "model.completed"
	EventModelFailed          EventKind = "model.failed"
	EventToolStarted          EventKind = "tool.started"
	EventToolProgress         EventKind = "tool.progress"
	EventToolCompleted        EventKind = "tool.completed"
	EventToolFailed           EventKind = "tool.failed"
	EventInteractionStarted   EventKind = "interaction.started"
	EventInteractionCompleted EventKind = "interaction.completed"
	EventInteractionFailed    EventKind = "interaction.failed"
	EventInteractionCancelled EventKind = "interaction.cancelled"
)

// MaximumModelDeltaBytes matches one kernel model stream delta.
const MaximumModelDeltaBytes = 256 << 10

// EventDetailKind identifies the active typed detail in EventDetail.
type EventDetailKind string

const (
	EventDetailNone                EventDetailKind = "none"
	EventDetailRunStarted          EventDetailKind = "run-started"
	EventDetailTurn                EventDetailKind = "turn"
	EventDetailModelStarted        EventDetailKind = "model-started"
	EventDetailText                EventDetailKind = "text"
	EventDetailModelCompleted      EventDetailKind = "model-completed"
	EventDetailModelFailed         EventDetailKind = "model-failed"
	EventDetailToolStarted         EventDetailKind = "tool-started"
	EventDetailToolProgress        EventDetailKind = "tool-progress"
	EventDetailToolTerminal        EventDetailKind = "tool-terminal"
	EventDetailInteractionStarted  EventDetailKind = "interaction-started"
	EventDetailInteractionTerminal EventDetailKind = "interaction-terminal"
	EventDetailStatus              EventDetailKind = "status"
)

// Usage is portable model token accounting. Provider metadata is deliberately
// omitted from the public client contract.
type Usage struct {
	inputTokens  uint64
	outputTokens uint64
}

func NewUsage(inputTokens, outputTokens uint64) Usage {
	return Usage{inputTokens: inputTokens, outputTokens: outputTokens}
}

func (usage Usage) InputTokens() uint64  { return usage.inputTokens }
func (usage Usage) OutputTokens() uint64 { return usage.outputTokens }

// ModelFailure is a portable model failure without provider metadata.
type ModelFailure struct {
	code         string
	message      string
	retryable    bool
	beforeStream bool
}

func NewModelFailure(code, message string, retryable, beforeStream bool) (ModelFailure, error) {
	if err := token("model failure code", code, 128); err != nil {
		return ModelFailure{}, err
	}
	if err := boundedText("model failure message", message, 4096, false); err != nil {
		return ModelFailure{}, err
	}
	return ModelFailure{code: code, message: message, retryable: retryable, beforeStream: beforeStream}, nil
}

func (failure ModelFailure) Code() string       { return failure.code }
func (failure ModelFailure) Message() string    { return failure.message }
func (failure ModelFailure) Retryable() bool    { return failure.retryable }
func (failure ModelFailure) BeforeStream() bool { return failure.beforeStream }

// ToolOutcome identifies whether a failed tool operation definitely committed
// no external mutation or has an uncertain external outcome.
type ToolOutcome string

const (
	ToolOutcomeDefinitive ToolOutcome = "definitive"
	ToolOutcomeUncertain  ToolOutcome = "uncertain"
)

// ToolRetry is portable deliberate-retry advice.
type ToolRetry string

const (
	ToolRetryNever   ToolRetry = "never"
	ToolRetryAllowed ToolRetry = "allowed"
)

// ToolTerminal is one compiled-tool completion or failure payload.
type ToolTerminal struct {
	callID  string
	name    string
	problem string
	outcome ToolOutcome
	retry   ToolRetry
}

func NewToolTerminal(callID, name, problem string, outcome ToolOutcome, retry ToolRetry) (ToolTerminal, error) {
	if err := token("tool call ID", callID, 128); err != nil {
		return ToolTerminal{}, err
	}
	if err := token("tool name", name, 128); err != nil {
		return ToolTerminal{}, err
	}
	if err := boundedText("tool terminal problem", problem, 4096, true); err != nil {
		return ToolTerminal{}, err
	}
	if err := validateToolOutcome(outcome, retry); err != nil {
		return ToolTerminal{}, err
	}
	return ToolTerminal{callID: callID, name: name, problem: problem, outcome: outcome, retry: retry}, nil
}

func (terminal ToolTerminal) CallID() string       { return terminal.callID }
func (terminal ToolTerminal) Name() string         { return terminal.name }
func (terminal ToolTerminal) Problem() string      { return terminal.problem }
func (terminal ToolTerminal) Outcome() ToolOutcome { return terminal.outcome }
func (terminal ToolTerminal) Retry() ToolRetry     { return terminal.retry }

func validateToolOutcome(outcome ToolOutcome, retry ToolRetry) error {
	if outcome == "" && retry == "" {
		return nil
	}
	if outcome != ToolOutcomeDefinitive && outcome != ToolOutcomeUncertain {
		return fmt.Errorf("tool outcome %q is unsupported", outcome)
	}
	if retry != ToolRetryNever && retry != ToolRetryAllowed {
		return fmt.Errorf("tool retry %q is unsupported", retry)
	}
	if outcome == ToolOutcomeUncertain && retry != ToolRetryNever {
		return errors.New("uncertain tool outcomes must never be retried")
	}
	return nil
}

// EventDetail is a closed immutable union that faithfully represents every
// current provider-neutral kernel event payload.
type EventDetail struct {
	kind            EventDetailKind
	definition      string
	turn            uint32
	operationID     string
	text            string
	usage           Usage
	modelFailure    ModelFailure
	callID          string
	toolName        string
	toolMessage     string
	toolTerminal    ToolTerminal
	interactionID   string
	interactionKind string
	status          string
}

func NoEventDetail() EventDetail { return EventDetail{kind: EventDetailNone} }

func NewRunStartedDetail(definition string) (EventDetail, error) {
	if err := token("run definition", definition, maximumTokenBytes); err != nil {
		return EventDetail{}, err
	}
	return EventDetail{kind: EventDetailRunStarted, definition: definition}, nil
}

func NewTurnDetail(turn uint32) (EventDetail, error) {
	if turn == 0 {
		return EventDetail{}, errors.New("event turn must be positive")
	}
	return EventDetail{kind: EventDetailTurn, turn: turn}, nil
}

func NewModelStartedDetail(turn uint32, operationID string) (EventDetail, error) {
	if turn == 0 {
		return EventDetail{}, errors.New("model start turn must be positive")
	}
	if err := token("model operation ID", operationID, 128); err != nil {
		return EventDetail{}, err
	}
	return EventDetail{kind: EventDetailModelStarted, turn: turn, operationID: operationID}, nil
}

func NewTextDetail(text string) (EventDetail, error) {
	if err := boundedText("event text", text, MaximumModelDeltaBytes, false); err != nil {
		return EventDetail{}, err
	}
	return EventDetail{kind: EventDetailText, text: text}, nil
}

func NewModelCompletedDetail(usage Usage) EventDetail {
	return EventDetail{kind: EventDetailModelCompleted, usage: usage}
}

func NewModelFailedDetail(failure ModelFailure) (EventDetail, error) {
	validated, err := NewModelFailure(failure.code, failure.message, failure.retryable, failure.beforeStream)
	if err != nil {
		return EventDetail{}, err
	}
	return EventDetail{kind: EventDetailModelFailed, modelFailure: validated}, nil
}

func NewToolStartedDetail(callID, name string) (EventDetail, error) {
	if err := token("tool call ID", callID, 128); err != nil {
		return EventDetail{}, err
	}
	if err := token("tool name", name, 128); err != nil {
		return EventDetail{}, err
	}
	return EventDetail{kind: EventDetailToolStarted, callID: callID, toolName: name}, nil
}

func NewToolProgressDetail(callID, message string) (EventDetail, error) {
	if err := token("tool progress call ID", callID, 128); err != nil {
		return EventDetail{}, err
	}
	if err := boundedText("tool progress message", message, 4096, false); err != nil {
		return EventDetail{}, err
	}
	return EventDetail{kind: EventDetailToolProgress, callID: callID, toolMessage: message}, nil
}

func NewToolTerminalDetail(terminal ToolTerminal) (EventDetail, error) {
	validated, err := NewToolTerminal(
		terminal.callID,
		terminal.name,
		terminal.problem,
		terminal.outcome,
		terminal.retry,
	)
	if err != nil {
		return EventDetail{}, err
	}
	return EventDetail{kind: EventDetailToolTerminal, toolTerminal: validated}, nil
}

func NewInteractionStartedDetail(id, kind string) (EventDetail, error) {
	if err := token("event interaction ID", id, 128); err != nil {
		return EventDetail{}, err
	}
	if err := token("event interaction kind", kind, 128); err != nil {
		return EventDetail{}, err
	}
	return EventDetail{kind: EventDetailInteractionStarted, interactionID: id, interactionKind: kind}, nil
}

func NewInteractionTerminalDetail(id, problem string) (EventDetail, error) {
	if err := token("event interaction ID", id, 128); err != nil {
		return EventDetail{}, err
	}
	if err := boundedText("interaction terminal problem", problem, maximumStatusBytes, true); err != nil {
		return EventDetail{}, err
	}
	return EventDetail{kind: EventDetailInteractionTerminal, interactionID: id, status: problem}, nil
}

func NewStatusDetail(message string) (EventDetail, error) {
	if err := boundedText("event status", message, MaximumTextBytes, false); err != nil {
		return EventDetail{}, err
	}
	return EventDetail{kind: EventDetailStatus, status: message}, nil
}

func (detail EventDetail) Kind() EventDetailKind { return detail.kind }
func (detail EventDetail) Definition() (string, bool) {
	return detail.definition, detail.kind == EventDetailRunStarted
}

func (detail EventDetail) Turn() (uint32, bool) {
	return detail.turn, detail.kind == EventDetailTurn
}

func (detail EventDetail) ModelStart() (uint32, string, bool) {
	return detail.turn, detail.operationID, detail.kind == EventDetailModelStarted
}

func (detail EventDetail) Text() (string, bool) {
	return detail.text, detail.kind == EventDetailText
}

func (detail EventDetail) Usage() (Usage, bool) {
	return detail.usage, detail.kind == EventDetailModelCompleted
}

func (detail EventDetail) ModelFailure() (ModelFailure, bool) {
	return detail.modelFailure, detail.kind == EventDetailModelFailed
}

func (detail EventDetail) ToolStart() (string, string, bool) {
	return detail.callID, detail.toolName, detail.kind == EventDetailToolStarted
}

func (detail EventDetail) ToolProgress() (string, string, bool) {
	return detail.callID, detail.toolMessage, detail.kind == EventDetailToolProgress
}

func (detail EventDetail) ToolTerminal() (ToolTerminal, bool) {
	return detail.toolTerminal, detail.kind == EventDetailToolTerminal
}

func (detail EventDetail) InteractionStart() (string, string, bool) {
	return detail.interactionID, detail.interactionKind, detail.kind == EventDetailInteractionStarted
}

func (detail EventDetail) InteractionTerminal() (string, string, bool) {
	return detail.interactionID, detail.status, detail.kind == EventDetailInteractionTerminal
}

func (detail EventDetail) Status() (string, bool) {
	return detail.status, detail.kind == EventDetailStatus
}

// Event is one strictly sequenced immutable client event.
type Event struct {
	run      RunRef
	sequence uint64
	at       time.Time
	kind     EventKind
	detail   EventDetail
}

func NewEvent(run RunRef, sequence uint64, at time.Time, kind EventKind, detail EventDetail) (Event, error) {
	if err := run.Validate(); err != nil {
		return Event{}, err
	}
	if sequence == 0 {
		return Event{}, errors.New("event sequence must be positive")
	}
	if at.IsZero() {
		return Event{}, errors.New("event timestamp must not be zero")
	}
	if !validEventKind(kind) {
		return Event{}, fmt.Errorf("event kind %q is unsupported", kind)
	}
	if err := validateDetailForEvent(kind, detail); err != nil {
		return Event{}, err
	}
	return Event{run: run, sequence: sequence, at: at.UTC(), kind: kind, detail: detail}, nil
}

func (current Event) Run() RunRef         { return current.run }
func (current Event) Sequence() uint64    { return current.sequence }
func (current Event) At() time.Time       { return current.at }
func (current Event) Kind() EventKind     { return current.kind }
func (current Event) Detail() EventDetail { return current.detail }

func (current Event) Terminal() bool {
	switch current.kind {
	case EventRunCompleted, EventRunFailed, EventRunCancelled,
		EventTurnCompleted, EventTurnFailed,
		EventModelCompleted, EventModelFailed,
		EventToolCompleted, EventToolFailed,
		EventInteractionCompleted, EventInteractionFailed, EventInteractionCancelled:
		return true
	default:
		return false
	}
}

// Cursor returns an acknowledgment candidate for this event. The caller must
// persist it only after processing the event successfully.
func (current Event) Cursor() Cursor {
	return Cursor{run: current.run, afterSequence: current.sequence}
}

func validEventKind(kind EventKind) bool {
	switch kind {
	case EventRunStarted, EventRunCompleted, EventRunFailed, EventRunCancelled,
		EventTurnStarted, EventTurnCompleted, EventTurnFailed,
		EventModelStarted, EventModelDelta, EventModelCompleted, EventModelFailed,
		EventToolStarted, EventToolProgress, EventToolCompleted, EventToolFailed,
		EventInteractionStarted, EventInteractionCompleted, EventInteractionFailed, EventInteractionCancelled:
		return true
	default:
		return false
	}
}

func validateDetailForEvent(kind EventKind, detail EventDetail) error {
	want := detailKindForEvent(kind)
	if detail.kind != want {
		return fmt.Errorf("event detail %q is invalid for kind %q", detail.kind, kind)
	}
	return validateTerminalDetail(kind, detail)
}

func detailKindForEvent(kind EventKind) EventDetailKind {
	switch kind {
	case EventRunStarted:
		return EventDetailRunStarted
	case EventRunFailed, EventRunCancelled, EventTurnFailed:
		return EventDetailStatus
	case EventTurnStarted, EventTurnCompleted:
		return EventDetailTurn
	case EventModelStarted:
		return EventDetailModelStarted
	case EventModelDelta:
		return EventDetailText
	case EventModelCompleted:
		return EventDetailModelCompleted
	case EventModelFailed:
		return EventDetailModelFailed
	case EventToolStarted:
		return EventDetailToolStarted
	case EventToolProgress:
		return EventDetailToolProgress
	case EventToolCompleted, EventToolFailed:
		return EventDetailToolTerminal
	case EventInteractionStarted:
		return EventDetailInteractionStarted
	case EventInteractionCompleted, EventInteractionFailed, EventInteractionCancelled:
		return EventDetailInteractionTerminal
	default:
		return EventDetailNone
	}
}

func validateTerminalDetail(kind EventKind, detail EventDetail) error {
	if kind == EventToolCompleted && detail.toolTerminal.problem != "" {
		return errors.New("completed tool event must not contain a problem")
	}
	if kind == EventToolFailed && detail.toolTerminal.problem == "" {
		return errors.New("failed tool event requires a problem")
	}
	if kind == EventInteractionCompleted && detail.status != "" {
		return errors.New("completed interaction event must not contain a problem")
	}
	if (kind == EventInteractionFailed || kind == EventInteractionCancelled) && detail.status == "" {
		return errors.New("failed interaction event requires a problem")
	}
	return nil
}
