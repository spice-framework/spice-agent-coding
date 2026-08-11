package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Format identifies a built-in caller-owned output format.
type Format string

const (
	// FormatJSON selects the canonical spice.log/v1 JSON encoding.
	FormatJSON Format = "json"
	// FormatText selects stable developer-oriented key-value text.
	FormatText Format = "text"

	// JSON is the canonical production format.
	JSON = FormatJSON
	// Text is the developer-oriented text format.
	Text = FormatText
)

// HandlerOptions configure built-in handlers.
type HandlerOptions struct{ AddSource bool }

type outputState struct {
	mu     sync.Mutex
	writer io.Writer
}

type storedAttribute struct {
	groups []string
	attr   slog.Attr
}

type canonicalHandler struct {
	format     Format
	options    HandlerOptions
	output     *outputState
	groups     []string
	attributes []storedAttribute
}

// NewJSONHandler constructs a canonical spice.log/v1 JSON slog handler.
func NewJSONHandler(writer io.Writer, options HandlerOptions) (slog.Handler, error) {
	return newCanonicalHandler(FormatJSON, writer, options)
}

// NewTextHandler constructs a stable developer-oriented text slog handler.
func NewTextHandler(writer io.Writer, options HandlerOptions) (slog.Handler, error) {
	return newCanonicalHandler(FormatText, writer, options)
}

func newCanonicalHandler(format Format, writer io.Writer, options HandlerOptions) (slog.Handler, error) {
	if writer == nil {
		return nil, errors.New("construct logging handler: writer is nil")
	}
	if format != FormatJSON && format != FormatText {
		return nil, fmt.Errorf("construct logging handler: format %q is unsupported", format)
	}
	return &canonicalHandler{format: format, options: options, output: &outputState{writer: writer}}, nil
}

func (*canonicalHandler) Enabled(context.Context, slog.Level) bool { return true }

func (handler *canonicalHandler) Handle(_ context.Context, record slog.Record) error {
	view := canonicalView{
		Schema: "spice.log/v1", Timestamp: record.Time.UTC(),
		Severity: record.Level.String(), Event: "slog.record", Message: record.Message,
		Attributes: make(map[string]any),
	}
	for _, item := range handler.attributes {
		view.add(item.groups, item.attr)
	}
	record.Attrs(func(attribute slog.Attr) bool {
		view.add(handler.groups, attribute)
		return true
	})
	if handler.options.AddSource && record.PC != 0 {
		frames := runtime.CallersFrames([]uintptr{record.PC})
		frame, _ := frames.Next()
		view.Attributes["source"] = filepath.Base(frame.File) + ":" + strconv.Itoa(frame.Line)
	}
	var content []byte
	var err error
	if handler.format == FormatJSON {
		content, err = json.Marshal(view)
		if err == nil {
			content = append(content, '\n')
		}
	} else {
		content = view.text()
	}
	if err != nil {
		return fmt.Errorf("encode structured log record: %w", err)
	}
	handler.output.mu.Lock()
	defer handler.output.mu.Unlock()
	if _, err = handler.output.writer.Write(content); err != nil {
		return fmt.Errorf("write structured log record: %w", err)
	}
	return nil
}

func (handler *canonicalHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	clone := *handler
	clone.groups = append([]string(nil), handler.groups...)
	clone.attributes = append([]storedAttribute(nil), handler.attributes...)
	for _, attribute := range attributes {
		clone.attributes = append(clone.attributes, storedAttribute{
			groups: append([]string(nil), handler.groups...), attr: attribute,
		})
	}
	return &clone
}

func (handler *canonicalHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return handler
	}
	clone := *handler
	clone.groups = append(append([]string(nil), handler.groups...), name)
	clone.attributes = append([]storedAttribute(nil), handler.attributes...)
	return &clone
}

type canonicalView struct {
	Schema      string         `json:"schema"`
	Timestamp   time.Time      `json:"timestamp"`
	Severity    string         `json:"severity"`
	Event       string         `json:"event"`
	Message     string         `json:"message"`
	Application string         `json:"application,omitempty"`
	Module      string         `json:"module,omitempty"`
	Component   string         `json:"component,omitempty"`
	TraceID     string         `json:"trace_id,omitempty"`
	SpanID      string         `json:"span_id,omitempty"`
	TraceFlags  uint8          `json:"trace_flags,omitempty"`
	Attributes  map[string]any `json:"attributes"`
}

func (view *canonicalView) add(groups []string, attribute slog.Attr) {
	attribute.Value = attribute.Value.Resolve()
	if len(groups) == 0 {
		switch attribute.Key {
		case "schema":
			view.Schema = attribute.Value.String()
			return
		case "event":
			view.Event = attribute.Value.String()
			return
		case "application":
			view.Application = attribute.Value.String()
			return
		case "module":
			view.Module = attribute.Value.String()
			return
		case "component":
			view.Component = attribute.Value.String()
			return
		case "trace_id":
			view.TraceID = attribute.Value.String()
			return
		case "span_id":
			view.SpanID = attribute.Value.String()
			return
		case "trace_flags":
			traceFlags := attribute.Value.Uint64()
			if traceFlags <= 255 {
				view.TraceFlags = uint8(traceFlags)
			}
			return
		case "attributes":
			if attribute.Value.Kind() == slog.KindGroup {
				for _, child := range attribute.Value.Group() {
					view.addAttribute(nil, child)
				}
				return
			}
		}
	}
	view.addAttribute(groups, attribute)
}

func (view *canonicalView) addAttribute(groups []string, attribute slog.Attr) {
	attribute.Value = attribute.Value.Resolve()
	if attribute.Value.Kind() == slog.KindGroup {
		next := groups
		if attribute.Key != "" {
			next = append(append([]string(nil), groups...), attribute.Key)
		}
		for _, child := range attribute.Value.Group() {
			view.addAttribute(next, child)
		}
		return
	}
	key := strings.Join(append(append([]string(nil), groups...), attribute.Key), ".")
	view.Attributes[key] = slogValue(attribute.Value)
}

func slogValue(value slog.Value) any {
	switch value.Kind() {
	case slog.KindBool:
		return value.Bool()
	case slog.KindDuration:
		return value.Duration().Nanoseconds()
	case slog.KindFloat64:
		return value.Float64()
	case slog.KindInt64:
		return value.Int64()
	case slog.KindString:
		return value.String()
	case slog.KindTime:
		return value.Time().UTC()
	case slog.KindUint64:
		return value.Uint64()
	case slog.KindAny:
		return value.Any()
	case slog.KindGroup:
		attributes := make(map[string]any, len(value.Group()))
		for _, attribute := range value.Group() {
			attributes[attribute.Key] = slogValue(attribute.Value.Resolve())
		}
		return attributes
	case slog.KindLogValuer:
		return slogValue(value.Resolve())
	default:
		return value.String()
	}
}

func (view *canonicalView) text() []byte {
	var buffer bytes.Buffer
	writeText := func(key string, value any) {
		if buffer.Len() != 0 {
			buffer.WriteByte(' ')
		}
		buffer.WriteString(key)
		buffer.WriteByte('=')
		buffer.WriteString(strconv.Quote(fmt.Sprint(value)))
	}
	writeText("schema", view.Schema)
	writeText("timestamp", view.Timestamp.Format(time.RFC3339Nano))
	writeText("severity", view.Severity)
	writeText("event", view.Event)
	writeText("message", view.Message)
	if view.Application != "" {
		writeText("application", view.Application)
	}
	if view.Module != "" {
		writeText("module", view.Module)
	}
	if view.Component != "" {
		writeText("component", view.Component)
	}
	if view.TraceID != "" {
		writeText("trace_id", view.TraceID)
	}
	if view.SpanID != "" {
		writeText("span_id", view.SpanID)
	}
	if view.TraceFlags != 0 {
		writeText("trace_flags", view.TraceFlags)
	}
	keys := make([]string, 0, len(view.Attributes))
	for key := range view.Attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeText(key, view.Attributes[key])
	}
	buffer.WriteByte('\n')
	return buffer.Bytes()
}
