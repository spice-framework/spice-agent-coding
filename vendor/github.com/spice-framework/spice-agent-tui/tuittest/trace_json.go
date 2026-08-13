package tuittest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	agenttui "github.com/spice-framework/spice-agent-tui"
)

type traceWire struct {
	Schema  string           `json:"schema"`
	Options traceOptionsWire `json:"options"`
	Events  []traceEventWire `json:"events"`
}

type traceOptionsWire struct {
	Width      int                `json:"width"`
	Height     int                `json:"height"`
	Accessible bool               `json:"accessible"`
	ThemeMode  agenttui.ThemeMode `json:"theme_mode"`
}

type traceEventWire struct {
	Type          TraceEventKind   `json:"type"`
	Text          *string          `json:"text,omitempty"`
	Stroke        *string          `json:"stroke,omitempty"`
	Action        *agenttui.Action `json:"action,omitempty"`
	Update        *traceUpdateWire `json:"update,omitempty"`
	Width         *int             `json:"width,omitempty"`
	Height        *int             `json:"height,omitempty"`
	Name          *string          `json:"name,omitempty"`
	ResultMessage *string          `json:"result_message,omitempty"`
}

type traceUpdateWire struct {
	Kind          agenttui.SessionUpdateKind `json:"kind"`
	Revision      uint64                     `json:"revision"`
	Snapshot      *traceSnapshotWire         `json:"snapshot,omitempty"`
	Activity      *string                    `json:"activity,omitempty"`
	PromptHistory *[]string                  `json:"prompt_history,omitempty"`
}

type traceSnapshotWire struct {
	Workspace     traceWorkspaceWire `json:"workspace"`
	Status        traceStatusWire    `json:"status"`
	Activity      []string           `json:"activity"`
	PromptHistory []string           `json:"prompt_history"`
}

type traceWorkspaceWire struct {
	Title    string             `json:"title"`
	Sections []traceSectionWire `json:"sections"`
}

type traceSectionWire struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type traceStatusWire struct {
	Level   agenttui.StatusLevel `json:"level"`
	Message string               `json:"message"`
	Hints   []string             `json:"hints"`
}

// ParseTrace accepts only the exact canonical JSON representation emitted by
// Trace.CanonicalJSON. Unknown fields, duplicate/noncanonical fields, trailing
// values, alternate whitespace, and a missing final LF are rejected.
func ParseTrace(content []byte) (Trace, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var wire traceWire
	if err := decoder.Decode(&wire); err != nil {
		return Trace{}, fmt.Errorf("decode trace: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Trace{}, errors.New("trace contains trailing JSON values")
	}
	trace, err := traceFromWire(wire)
	if err != nil {
		return Trace{}, err
	}
	if !bytes.Equal(content, trace.CanonicalJSON()) {
		return Trace{}, errors.New("trace JSON is valid but not canonical")
	}
	return trace, nil
}

func marshalTrace(trace Trace) []byte {
	wire := traceToWire(trace)
	content, err := json.Marshal(wire)
	if err != nil {
		panic("marshal validated trace: " + err.Error())
	}
	return append(content, '\n')
}

func traceToWire(trace Trace) traceWire {
	events := make([]traceEventWire, 0, len(trace.events))
	for _, event := range trace.events {
		events = append(events, eventToWire(event))
	}
	return traceWire{
		Schema: traceSchema,
		Options: traceOptionsWire{
			Width: trace.options.Width, Height: trace.options.Height,
			Accessible: trace.options.Accessible, ThemeMode: trace.options.ThemeMode,
		},
		Events: events,
	}
}

func eventToWire(event TraceEvent) traceEventWire {
	wire := traceEventWire{Type: event.kind}
	switch event.kind {
	case TraceEventType:
		wire.Text = new(event.text)
	case TraceEventKey:
		wire.Stroke = new(event.stroke)
		if event.text != "" {
			wire.Text = new(event.text)
		}
	case TraceEventAction:
		wire.Action = new(event.action)
	case TraceEventUpdate:
		update := updateToWire(event.update)
		wire.Update = &update
	case TraceEventResize:
		wire.Width = new(event.width)
		wire.Height = new(event.height)
	case TraceEventSnapshot:
		wire.Name = new(event.name)
	case TraceEventPerformResult:
		wire.ResultMessage = new(event.result.Message().String())
	}
	return wire
}

func updateToWire(update agenttui.SessionUpdate) traceUpdateWire {
	wire := traceUpdateWire{Kind: update.Kind(), Revision: update.Revision()}
	switch update.Kind() {
	case agenttui.SessionUpdateSnapshot:
		snapshot, _ := update.Snapshot()
		workspace := snapshot.Workspace()
		sections := make([]traceSectionWire, 0, len(workspace.Sections()))
		for _, section := range workspace.Sections() {
			sections = append(sections, traceSectionWire{
				Title: section.Title().String(), Body: section.Body().String(),
			})
		}
		status := snapshot.Status()
		payload := traceSnapshotWire{
			Workspace: traceWorkspaceWire{Title: workspace.Title().String(), Sections: sections},
			Status: traceStatusWire{
				Level: status.Level(), Message: status.Message().String(), Hints: textStrings(status.Hints()),
			},
			Activity: textStrings(snapshot.Activity()), PromptHistory: textStrings(snapshot.PromptHistory()),
		}
		wire.Snapshot = &payload
	case agenttui.SessionUpdateActivity:
		activity, _ := update.Activity()
		wire.Activity = new(activity.String())
	case agenttui.SessionUpdatePromptHistory:
		history, _ := update.PromptHistory()
		values := textStrings(history)
		wire.PromptHistory = &values
	}
	return wire
}

func traceFromWire(wire traceWire) (Trace, error) {
	if wire.Schema != traceSchema {
		return Trace{}, fmt.Errorf("unsupported trace schema %q", wire.Schema)
	}
	events := make([]TraceEvent, 0, len(wire.Events))
	for index, eventWire := range wire.Events {
		event, err := eventFromWire(eventWire)
		if err != nil {
			return Trace{}, fmt.Errorf("decode trace event %d: %w", index, err)
		}
		events = append(events, event)
	}
	return NewTrace(TraceOptions{
		Width: wire.Options.Width, Height: wire.Options.Height,
		Accessible: wire.Options.Accessible, ThemeMode: wire.Options.ThemeMode,
	}, events)
}

func eventFromWire(wire traceEventWire) (TraceEvent, error) {
	switch wire.Type {
	case TraceEventType:
		return typeEventFromWire(wire)
	case TraceEventKey:
		return keyEventFromWire(wire)
	case TraceEventAction:
		return actionEventFromWire(wire)
	case TraceEventUpdate:
		return updateEventFromWire(wire)
	case TraceEventResize:
		return resizeEventFromWire(wire)
	case TraceEventSnapshot:
		return snapshotEventFromWire(wire)
	case TraceEventPerformResult:
		return resultEventFromWire(wire)
	default:
		return TraceEvent{}, fmt.Errorf("unsupported event type %q", wire.Type)
	}
}

func typeEventFromWire(wire traceEventWire) (TraceEvent, error) {
	if wire.Text == nil || hasEventFields(wire, "text") {
		return TraceEvent{}, errors.New("type event requires only text")
	}
	return NewTypeEvent(*wire.Text)
}

func keyEventFromWire(wire traceEventWire) (TraceEvent, error) {
	if wire.Stroke == nil || hasEventFields(wire, "stroke", "text") {
		return TraceEvent{}, errors.New("key event requires stroke and optional text")
	}
	if wire.Text == nil {
		return NewKeyEvent(*wire.Stroke)
	}
	return NewKeyEvent(*wire.Stroke, *wire.Text)
}

func actionEventFromWire(wire traceEventWire) (TraceEvent, error) {
	if wire.Action == nil || hasEventFields(wire, "action") {
		return TraceEvent{}, errors.New("action event requires only action")
	}
	return NewActionEvent(*wire.Action)
}

func updateEventFromWire(wire traceEventWire) (TraceEvent, error) {
	if wire.Update == nil || hasEventFields(wire, "update") {
		return TraceEvent{}, errors.New("update event requires only update")
	}
	update, err := updateFromWire(*wire.Update)
	if err != nil {
		return TraceEvent{}, err
	}
	return NewUpdateEvent(update)
}

func resizeEventFromWire(wire traceEventWire) (TraceEvent, error) {
	if wire.Width == nil || wire.Height == nil || hasEventFields(wire, "width", "height") {
		return TraceEvent{}, errors.New("resize event requires only width and height")
	}
	return NewResizeEvent(*wire.Width, *wire.Height)
}

func snapshotEventFromWire(wire traceEventWire) (TraceEvent, error) {
	if wire.Name == nil || hasEventFields(wire, "name") {
		return TraceEvent{}, errors.New("snapshot event requires only name")
	}
	return NewSnapshotEvent(*wire.Name)
}

func resultEventFromWire(wire traceEventWire) (TraceEvent, error) {
	if wire.ResultMessage == nil || hasEventFields(wire, "result_message") {
		return TraceEvent{}, errors.New("perform-result event requires only result_message")
	}
	message, err := agenttui.NewText(*wire.ResultMessage)
	if err != nil {
		return TraceEvent{}, err
	}
	result, err := agenttui.NewCommandResult(message, nil)
	if err != nil {
		return TraceEvent{}, err
	}
	return NewPerformResultEvent(result)
}

func updateFromWire(wire traceUpdateWire) (agenttui.SessionUpdate, error) {
	switch wire.Kind {
	case agenttui.SessionUpdateSnapshot:
		return snapshotUpdateFromWire(wire)
	case agenttui.SessionUpdateActivity:
		return activityUpdateFromWire(wire)
	case agenttui.SessionUpdatePromptHistory:
		return historyUpdateFromWire(wire)
	default:
		return agenttui.SessionUpdate{}, fmt.Errorf("unsupported session update kind %q", wire.Kind)
	}
}

func snapshotUpdateFromWire(wire traceUpdateWire) (agenttui.SessionUpdate, error) {
	if wire.Snapshot == nil || wire.Activity != nil || wire.PromptHistory != nil {
		return agenttui.SessionUpdate{}, errors.New("snapshot update requires only snapshot payload")
	}
	snapshot, err := snapshotFromWire(wire.Revision, *wire.Snapshot)
	if err != nil {
		return agenttui.SessionUpdate{}, err
	}
	return agenttui.NewSnapshotUpdate(snapshot)
}

func activityUpdateFromWire(wire traceUpdateWire) (agenttui.SessionUpdate, error) {
	if wire.Activity == nil || wire.Snapshot != nil || wire.PromptHistory != nil {
		return agenttui.SessionUpdate{}, errors.New("activity update requires only activity payload")
	}
	activity, err := agenttui.NewText(*wire.Activity)
	if err != nil {
		return agenttui.SessionUpdate{}, err
	}
	return agenttui.NewActivityUpdate(wire.Revision, activity)
}

func historyUpdateFromWire(wire traceUpdateWire) (agenttui.SessionUpdate, error) {
	if wire.PromptHistory == nil || wire.Snapshot != nil || wire.Activity != nil {
		return agenttui.SessionUpdate{}, errors.New("prompt-history update requires only prompt_history payload")
	}
	history, err := stringsToTexts(*wire.PromptHistory)
	if err != nil {
		return agenttui.SessionUpdate{}, err
	}
	return agenttui.NewPromptHistoryUpdate(wire.Revision, history)
}

func snapshotFromWire(revision uint64, wire traceSnapshotWire) (agenttui.SessionSnapshot, error) {
	sections := make([]agenttui.Section, 0, len(wire.Workspace.Sections))
	for index, candidate := range wire.Workspace.Sections {
		title, err := agenttui.NewText(candidate.Title)
		if err != nil {
			return agenttui.SessionSnapshot{}, fmt.Errorf("workspace section %d title: %w", index, err)
		}
		body, err := agenttui.NewText(candidate.Body)
		if err != nil {
			return agenttui.SessionSnapshot{}, fmt.Errorf("workspace section %d body: %w", index, err)
		}
		section, err := agenttui.NewSection(title, body)
		if err != nil {
			return agenttui.SessionSnapshot{}, fmt.Errorf("workspace section %d: %w", index, err)
		}
		sections = append(sections, section)
	}
	title, err := agenttui.NewText(wire.Workspace.Title)
	if err != nil {
		return agenttui.SessionSnapshot{}, err
	}
	workspace, err := agenttui.NewWorkspace(title, sections)
	if err != nil {
		return agenttui.SessionSnapshot{}, err
	}
	message, err := agenttui.NewText(wire.Status.Message)
	if err != nil {
		return agenttui.SessionSnapshot{}, err
	}
	hints, err := stringsToTexts(wire.Status.Hints)
	if err != nil {
		return agenttui.SessionSnapshot{}, err
	}
	status, err := agenttui.NewStatus(wire.Status.Level, message, hints)
	if err != nil {
		return agenttui.SessionSnapshot{}, err
	}
	activity, err := stringsToTexts(wire.Activity)
	if err != nil {
		return agenttui.SessionSnapshot{}, err
	}
	history, err := stringsToTexts(wire.PromptHistory)
	if err != nil {
		return agenttui.SessionSnapshot{}, err
	}
	return agenttui.NewSessionSnapshot(revision, workspace, status, activity, history)
}

func hasEventFields(wire traceEventWire, allowed ...string) bool {
	fields := []struct {
		name    string
		present bool
	}{
		{name: "text", present: wire.Text != nil},
		{name: "stroke", present: wire.Stroke != nil},
		{name: "action", present: wire.Action != nil},
		{name: "update", present: wire.Update != nil},
		{name: "width", present: wire.Width != nil},
		{name: "height", present: wire.Height != nil},
		{name: "name", present: wire.Name != nil},
		{name: "result_message", present: wire.ResultMessage != nil},
	}
	for _, field := range fields {
		if field.present && !slices.Contains(allowed, field.name) {
			return true
		}
	}
	return false
}

func textStrings(values []agenttui.Text) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.String())
	}
	return result
}

func stringsToTexts(values []string) ([]agenttui.Text, error) {
	result := make([]agenttui.Text, 0, len(values))
	for index, value := range values {
		text, err := agenttui.NewText(value)
		if err != nil {
			return nil, fmt.Errorf("text item %d: %w", index, err)
		}
		result = append(result, text)
	}
	return result, nil
}
