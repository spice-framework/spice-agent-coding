package tuittest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	agenttui "github.com/spice-framework/spice-agent-tui"
)

const (
	traceSchema        = "spice-agent-tui/tuittest-trace/v1alpha1"
	maximumTraceEvents = 256
)

// TraceOptions are the complete deterministic presentation inputs for a Trace.
type TraceOptions struct {
	Width      int
	Height     int
	Accessible bool
	ThemeMode  agenttui.ThemeMode
}

// TraceEventKind is the stable JSON tag for one trace event.
type TraceEventKind string

const (
	TraceEventType          TraceEventKind = "type"
	TraceEventKey           TraceEventKind = "key"
	TraceEventAction        TraceEventKind = "action"
	TraceEventUpdate        TraceEventKind = "update"
	TraceEventResize        TraceEventKind = "resize"
	TraceEventSnapshot      TraceEventKind = "snapshot"
	TraceEventPerformResult TraceEventKind = "perform-result"
)

// TraceEvent is one immutable, tagged interaction input.
type TraceEvent struct {
	kind   TraceEventKind
	text   string
	stroke string
	action agenttui.Action
	update agenttui.SessionUpdate
	width  int
	height int
	name   string
	result agenttui.CommandResult
}

// NewTypeEvent constructs one grapheme-aware text insertion event.
func NewTypeEvent(text string) (TraceEvent, error) {
	event := TraceEvent{kind: TraceEventType, text: text}
	return event, event.validate()
}

// NewKeyEvent constructs one named or printable key event.
func NewKeyEvent(stroke string, text ...string) (TraceEvent, error) {
	if len(text) > 1 {
		return TraceEvent{}, errors.New("trace key event accepts at most one text value")
	}
	payload := ""
	if len(text) == 1 {
		payload = text[0]
	}
	event := TraceEvent{kind: TraceEventKey, stroke: stroke, text: payload}
	return event, event.validate()
}

// NewActionEvent constructs one semantic key-binding event.
func NewActionEvent(action agenttui.Action) (TraceEvent, error) {
	event := TraceEvent{kind: TraceEventAction, action: action}
	return event, event.validate()
}

// NewUpdateEvent constructs one validated session update event.
func NewUpdateEvent(update agenttui.SessionUpdate) (TraceEvent, error) {
	event := TraceEvent{kind: TraceEventUpdate, update: update}
	return event, event.validate()
}

// NewResizeEvent constructs one bounded terminal resize event.
func NewResizeEvent(width, height int) (TraceEvent, error) {
	event := TraceEvent{kind: TraceEventResize, width: width, height: height}
	return event, event.validate()
}

// NewSnapshotEvent constructs one named committed-golden checkpoint.
func NewSnapshotEvent(name string) (TraceEvent, error) {
	event := TraceEvent{kind: TraceEventSnapshot, name: name}
	return event, event.validate()
}

// NewPerformResultEvent configures the result returned by later effect events.
// Nested result intents are rejected because presentation rejects them too.
func NewPerformResultEvent(result agenttui.CommandResult) (TraceEvent, error) {
	event := TraceEvent{kind: TraceEventPerformResult, result: result}
	return event, event.validate()
}

// Kind returns the stable event tag.
func (event TraceEvent) Kind() TraceEventKind { return event.kind }

// Trace is one immutable canonical interaction recording.
type Trace struct {
	options TraceOptions
	events  []TraceEvent
}

// NewTrace validates and defensively copies a deterministic interaction trace.
func NewTrace(options TraceOptions, events []TraceEvent) (Trace, error) {
	options = normalizeTraceOptions(options)
	trace := Trace{options: options, events: append([]TraceEvent(nil), events...)}
	return trace, trace.validate()
}

// Options returns the immutable driver configuration.
func (trace Trace) Options() TraceOptions { return trace.options }

// Events returns a defensive copy of the ordered tagged events.
func (trace Trace) Events() []TraceEvent { return append([]TraceEvent(nil), trace.events...) }

// CanonicalJSON returns compact, LF-terminated, fixed-field-order JSON.
func (trace Trace) CanonicalJSON() []byte { return marshalTrace(trace) }

// Digest returns a SHA-256 digest of CanonicalJSON.
func (trace Trace) Digest() string {
	sum := sha256.Sum256(trace.CanonicalJSON())
	return hex.EncodeToString(sum[:])
}

func normalizeTraceOptions(options TraceOptions) TraceOptions {
	if options.Width == 0 {
		options.Width = 48
	}
	if options.Height == 0 {
		options.Height = 12
	}
	if options.ThemeMode == "" {
		options.ThemeMode = agenttui.ThemeDark
	}
	return options
}

func (trace Trace) validate() error {
	if _, err := agenttui.NewSize(trace.options.Width, trace.options.Height); err != nil {
		return fmt.Errorf("trace options: %w", err)
	}
	if trace.options.ThemeMode != agenttui.ThemeDark && trace.options.ThemeMode != agenttui.ThemeLight {
		return fmt.Errorf("trace options: unsupported theme mode %q", trace.options.ThemeMode)
	}
	if len(trace.events) == 0 {
		return errors.New("trace must contain at least one event")
	}
	if len(trace.events) > maximumTraceEvents {
		return fmt.Errorf("trace events exceed %d", maximumTraceEvents)
	}
	snapshots := make(map[string]int)
	var revision uint64
	performResultConfigured := false
	for index, event := range trace.events {
		if err := event.validate(); err != nil {
			return fmt.Errorf("trace event %d (%s): %w", index, event.kind, err)
		}
		if event.kind != TraceEventSnapshot {
			if event.kind == TraceEventPerformResult {
				performResultConfigured = true
			}
			if traceEventMayPerform(event) && !performResultConfigured {
				return fmt.Errorf("trace event %d (%s) requires an earlier perform-result event", index, event.kind)
			}
			if event.kind == TraceEventUpdate {
				if event.update.Revision() <= revision {
					return fmt.Errorf(
						"trace event %d update revision %d is not greater than %d",
						index, event.update.Revision(), revision,
					)
				}
				revision = event.update.Revision()
			}
			continue
		}
		if previous, exists := snapshots[event.name]; exists {
			return fmt.Errorf("trace snapshot %q is duplicated at events %d and %d", event.name, previous, index)
		}
		snapshots[event.name] = index
	}
	return nil
}

func (event TraceEvent) validate() error {
	switch event.kind {
	case TraceEventType:
		return validateTraceType(event.text)
	case TraceEventKey:
		return validateTraceKey(event.stroke, event.text)
	case TraceEventAction:
		return validateTraceAction(event.action)
	case TraceEventUpdate:
		return validateTraceUpdate(event.update)
	case TraceEventResize:
		return validateTraceSize(event.width, event.height)
	case TraceEventSnapshot:
		return validateTraceName(event.name)
	case TraceEventPerformResult:
		return validateTraceResult(event.result)
	default:
		return fmt.Errorf("unsupported trace event kind %q", event.kind)
	}
}

func validateTraceType(text string) error {
	if text == "" {
		return errors.New("typed text must not be empty")
	}
	if !utf8.ValidString(text) || strings.ContainsAny(text, "\n\t\x1b") {
		return errors.New("typed text must be valid UTF-8 without control characters")
	}
	if len(text) > agenttui.MaximumPromptBytes {
		return fmt.Errorf("typed text exceeds %d bytes", agenttui.MaximumPromptBytes)
	}
	return nil
}

func validateTraceKey(stroke, text string) error {
	if _, err := keyPress(stroke, text); err != nil {
		return fmt.Errorf("key: %w", err)
	}
	return nil
}

func validateTraceAction(action agenttui.Action) error {
	if !validTraceAction(action) {
		return fmt.Errorf("unsupported action %q", action)
	}
	return nil
}

func validateTraceUpdate(update agenttui.SessionUpdate) error {
	if err := update.Validate(); err != nil {
		return fmt.Errorf("session update: %w", err)
	}
	return nil
}

func validateTraceSize(width, height int) error {
	_, err := agenttui.NewSize(width, height)
	return err
}

func validateTraceResult(result agenttui.CommandResult) error {
	if err := result.Validate(); err != nil {
		return fmt.Errorf("perform result: %w", err)
	}
	if _, exists := result.Intent(); exists {
		return errors.New("perform result must not contain a nested intent")
	}
	return nil
}

func traceEventMayPerform(event TraceEvent) bool {
	if event.kind == TraceEventAction {
		return event.action == agenttui.ActionSubmit || event.action == agenttui.ActionRespond ||
			event.action == agenttui.ActionCancelActiveRun
	}
	if event.kind != TraceEventKey {
		return false
	}
	switch event.stroke {
	case "enter", "return", "alt+enter", "esc", "escape", "ctrl+x":
		return true
	default:
		return false
	}
}

func validTraceAction(action agenttui.Action) bool {
	switch action {
	case agenttui.ActionQuit, agenttui.ActionSubmit, agenttui.ActionCancelActiveRun,
		agenttui.ActionRespond, agenttui.ActionCursorLeft, agenttui.ActionCursorRight,
		agenttui.ActionCursorStart, agenttui.ActionCursorEnd,
		agenttui.ActionHistoryPrevious, agenttui.ActionHistoryNext, agenttui.ActionBackspace:
		return true
	default:
		return false
	}
}

func validateTraceName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || len(name) > 128 || !utf8.ValidString(name) {
		return errors.New("snapshot name must be non-empty, trimmed, valid UTF-8, and at most 128 bytes")
	}
	if strings.ContainsAny(name, "\n\r\t\x1b/\\") {
		return errors.New("snapshot name must not contain controls or path separators")
	}
	return nil
}
