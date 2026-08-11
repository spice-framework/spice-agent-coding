package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

const (
	maximumEventNameBytes        = 128
	maximumFieldKeyBytes         = 64
	maximumFields                = 64
	maximumMessageBytes          = 4 << 10
	maximumStringValueBytes      = 16 << 10
	maximumSafeErrorMessageBytes = 1 << 10
)

var (
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	eventPattern      = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	traceIDPattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	spanIDPattern     = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

var reservedFields = map[string]struct{}{
	"schema": {}, "timestamp": {}, "severity": {}, "event": {},
	"message": {}, "application": {}, "module": {}, "component": {},
	"trace_id": {}, "span_id": {}, "trace_flags": {}, "attributes": {},
}

// Correlation carries optional W3C-compatible trace identity without owning a
// tracing SDK or extracting process-global context.
type Correlation struct {
	TraceID    string
	SpanID     string
	TraceFlags uint8
}

// Record is one validated Spice-native structured event.
type Record struct {
	Timestamp   time.Time
	Level       Level
	Event       string
	Message     string
	Scope       Scope
	Correlation Correlation
	Fields      []Field
}

// Configuration is resolved before one logger is constructed.
type Configuration struct {
	Format    Format
	Level     Level
	Levels    []LevelRule
	AddSource bool
}

// Options construct one application-owned logger. Writer and Handler are
// mutually exclusive; omitting both creates an explicit discard logger.
type Options struct {
	Application   string
	Configuration Configuration
	Writer        io.Writer
	Handler       slog.Handler
	Scopes        []Scope
}

type loggerStats struct {
	attempted atomic.Uint64
	emitted   atomic.Uint64
	filtered  atomic.Uint64
	failed    atomic.Uint64
}

// Stats is an immutable logger accounting snapshot.
type Stats struct {
	Attempted uint64 `json:"attempted"`
	Emitted   uint64 `json:"emitted"`
	Filtered  uint64 `json:"filtered"`
	Failed    uint64 `json:"failed"`
}

// Logger emits through one standard slog.Handler without mutating globals.
type Logger struct {
	application string
	handler     slog.Handler
	controller  *Controller
	scope       Scope
	addSource   bool
	stats       *loggerStats
}

// New validates and constructs one application logger.
func New(options Options) (*Logger, error) {
	configuration, err := validateOptions(options)
	if err != nil {
		return nil, err
	}
	controller, err := newController(configuration.Level, options.Scopes, configuration.Levels)
	if err != nil {
		return nil, fmt.Errorf("construct logger: %w", err)
	}
	handler, err := resolveHandler(options, configuration)
	if err != nil {
		return nil, err
	}
	return &Logger{
		application: options.Application, handler: handler, controller: controller,
		addSource: configuration.AddSource, stats: &loggerStats{},
	}, nil
}

func validateOptions(options Options) (Configuration, error) {
	if strings.TrimSpace(options.Application) == "" || strings.ContainsAny(options.Application, "\x00\r\n\t") {
		return Configuration{}, errors.New("construct logger: application identity is invalid")
	}
	if options.Writer != nil && options.Handler != nil {
		return Configuration{}, errors.New("construct logger: writer and handler are mutually exclusive")
	}
	configuration := options.Configuration
	if configuration.Format == "" {
		configuration.Format = FormatJSON
	}
	if configuration.Format != FormatJSON && configuration.Format != FormatText {
		return Configuration{}, fmt.Errorf("construct logger: format %q is unsupported", configuration.Format)
	}
	if !configuration.Level.validConfiguredLevel() {
		return Configuration{}, fmt.Errorf("construct logger: level %d is invalid", configuration.Level)
	}
	return configuration, nil
}

func resolveHandler(options Options, configuration Configuration) (slog.Handler, error) {
	if options.Handler != nil {
		return options.Handler, nil
	}
	if options.Writer == nil {
		return slog.DiscardHandler, nil
	}
	handlerOptions := HandlerOptions{AddSource: configuration.AddSource}
	var handler slog.Handler
	var err error
	if configuration.Format == FormatJSON {
		handler, err = NewJSONHandler(options.Writer, handlerOptions)
	} else {
		handler, err = NewTextHandler(options.Writer, handlerOptions)
	}
	if err != nil {
		return nil, fmt.Errorf("construct logger: %w", err)
	}
	return handler, nil
}

// Emit validates and writes one record. Handler errors are returned and
// counted; no secondary log is emitted.
func (logger *Logger) Emit(ctx context.Context, record Record) error {
	if logger == nil {
		return errors.New("emit structured log record: logger is nil")
	}
	logger.stats.attempted.Add(1)
	if ctx == nil {
		return logger.fail(errors.New("emit structured log record: context is nil"))
	}
	scope, err := logger.resolveScope(record.Scope)
	if err != nil {
		return logger.fail(err)
	}
	if err := validateRecord(record, scope); err != nil {
		return logger.fail(fmt.Errorf("emit structured log record: %w", err))
	}
	if !logger.controller.registered(scope) {
		return logger.fail(fmt.Errorf("emit structured log record: scope %q is not registered", scope.ID()))
	}
	if !logger.controller.enabled(scope, record.Level) ||
		!logger.handler.Enabled(ctx, record.Level.slog()) {
		logger.stats.filtered.Add(1)
		return nil
	}
	slogRecord := logger.newSlogRecord(record, scope)
	if err := handleRecord(logger.handler, ctx, slogRecord); err != nil {
		return logger.fail(err)
	}
	logger.stats.emitted.Add(1)
	return nil
}

func (logger *Logger) resolveScope(recordScope Scope) (Scope, error) {
	loggerID := logger.scope.ID()
	recordID := recordScope.ID()
	if recordID == "root" && loggerID != "root" {
		return logger.scope, nil
	}
	if loggerID != "root" && recordID != loggerID {
		return Scope{}, fmt.Errorf("emit structured log record: scope %q does not match logger scope %q", recordID, loggerID)
	}
	return recordScope, nil
}

func (logger *Logger) newSlogRecord(record Record, scope Scope) slog.Record {
	timestamp := record.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	result := slog.NewRecord(timestamp, record.Level.slog(), record.Message, 0)
	if logger.addSource {
		var callers [1]uintptr
		if runtime.Callers(3, callers[:]) == 1 {
			result.PC = callers[0]
		}
	}
	result.AddAttrs(
		slog.String("schema", "spice.log/v1"),
		slog.String("event", record.Event),
		slog.String("application", logger.application),
	)
	result.AddAttrs(scopeAttributes(scope)...)
	result.AddAttrs(correlationAttributes(record.Correlation)...)
	attributes := make([]any, len(record.Fields))
	for index, field := range record.Fields {
		attributes[index] = field.attribute
	}
	if len(attributes) != 0 {
		result.AddAttrs(slog.Group("attributes", attributes...))
	}
	return result
}

func scopeAttributes(scope Scope) []slog.Attr {
	attributes := make([]slog.Attr, 0, 2)
	if scope.Module != "" {
		attributes = append(attributes, slog.String("module", scope.Module))
	}
	if scope.Component != "" {
		attributes = append(attributes, slog.String("component", scope.Component))
	}
	return attributes
}

func correlationAttributes(correlation Correlation) []slog.Attr {
	attributes := make([]slog.Attr, 0, 3)
	if correlation.TraceID != "" {
		attributes = append(attributes, slog.String("trace_id", correlation.TraceID))
	}
	if correlation.SpanID != "" {
		attributes = append(attributes, slog.String("span_id", correlation.SpanID))
	}
	if correlation.TraceFlags != 0 {
		attributes = append(attributes, slog.Uint64("trace_flags", uint64(correlation.TraceFlags)))
	}
	return attributes
}

// Trace emits a trace record in this logger's scope.
func (logger *Logger) Trace(ctx context.Context, event, message string, fields ...Field) error {
	return logger.Emit(ctx, Record{Level: LevelTrace, Event: event, Message: message, Fields: fields})
}

// Debug emits a debug record in this logger's scope.
func (logger *Logger) Debug(ctx context.Context, event, message string, fields ...Field) error {
	return logger.Emit(ctx, Record{Level: LevelDebug, Event: event, Message: message, Fields: fields})
}

// Info emits an informational record in this logger's scope.
func (logger *Logger) Info(ctx context.Context, event, message string, fields ...Field) error {
	return logger.Emit(ctx, Record{Level: LevelInfo, Event: event, Message: message, Fields: fields})
}

// Warn emits a warning record in this logger's scope.
func (logger *Logger) Warn(ctx context.Context, event, message string, fields ...Field) error {
	return logger.Emit(ctx, Record{Level: LevelWarn, Event: event, Message: message, Fields: fields})
}

// Error emits an error record in this logger's scope.
func (logger *Logger) Error(ctx context.Context, event, message string, fields ...Field) error {
	return logger.Emit(ctx, Record{Level: LevelError, Event: event, Message: message, Fields: fields})
}

// WithScope returns an immutable logger for one registered exact scope.
func (logger *Logger) WithScope(scope Scope) (*Logger, error) {
	if logger == nil {
		return nil, errors.New("scope logger: logger is nil")
	}
	if err := scope.validate(); err != nil {
		return nil, fmt.Errorf("scope logger: %w", err)
	}
	if !logger.controller.registered(scope) {
		return nil, fmt.Errorf("scope logger: scope %q is not registered", scope.ID())
	}
	clone := *logger
	clone.scope = scope
	return &clone, nil
}

// Controller returns this logger's exact-scope level controller.
func (logger *Logger) Controller() *Controller {
	if logger == nil {
		return nil
	}
	return logger.controller
}

// Stats returns a concurrent-safe immutable accounting snapshot.
func (logger *Logger) Stats() Stats {
	if logger == nil {
		return Stats{}
	}
	return Stats{
		Attempted: logger.stats.attempted.Load(), Emitted: logger.stats.emitted.Load(),
		Filtered: logger.stats.filtered.Load(), Failed: logger.stats.failed.Load(),
	}
}

// Slog returns an instance-owned compatibility adapter. Generic slog values
// are outside the safe Field contract and remain the caller's responsibility.
func (logger *Logger) Slog() *slog.Logger {
	if logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(&slogAdapter{logger: logger})
}

func (logger *Logger) fail(err error) error {
	logger.stats.failed.Add(1)
	return err
}

func handleRecord(handler slog.Handler, ctx context.Context, record slog.Record) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("structured logging handler panicked")
		}
	}()
	return handler.Handle(ctx, record)
}

func validateRecord(record Record, scope Scope) error {
	if !record.Level.validRecordLevel() {
		return fmt.Errorf("level %d is invalid", record.Level)
	}
	if !validEventName(record.Event) {
		return fmt.Errorf("event %q is invalid", record.Event)
	}
	if len(record.Message) == 0 || len(record.Message) > maximumMessageBytes || strings.ContainsRune(record.Message, '\x00') {
		return errors.New("message must be non-empty, bounded, and contain no NUL")
	}
	if err := scope.validate(); err != nil {
		return err
	}
	if err := validateFields(record.Fields); err != nil {
		return err
	}
	return validateCorrelation(record.Correlation)
}

func validateFields(fields []Field) error {
	if len(fields) > maximumFields {
		return fmt.Errorf("field count exceeds %d", maximumFields)
	}
	seen := make(map[string]struct{}, len(fields))
	for index, field := range fields {
		key := field.attribute.Key
		if !validIdentifier(key, maximumFieldKeyBytes) {
			return fmt.Errorf("field %d key %q is invalid", index, key)
		}
		if _, reserved := reservedFields[key]; reserved {
			return fmt.Errorf("field %d key %q is reserved", index, key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("field key %q is duplicated", key)
		}
		seen[key] = struct{}{}
		value := field.attribute.Value.Resolve()
		if value.Kind() == slog.KindString && len(value.String()) > maximumStringValueBytes {
			return fmt.Errorf("field %q string value exceeds %d bytes", key, maximumStringValueBytes)
		}
	}
	return nil
}

func validateCorrelation(correlation Correlation) error {
	if correlation.SpanID != "" && correlation.TraceID == "" {
		return errors.New("span correlation requires a trace ID")
	}
	if correlation.TraceID != "" && !traceIDPattern.MatchString(correlation.TraceID) {
		return errors.New("trace ID must be 32 lowercase hexadecimal characters")
	}
	if correlation.SpanID != "" && !spanIDPattern.MatchString(correlation.SpanID) {
		return errors.New("span ID must be 16 lowercase hexadecimal characters")
	}
	return nil
}

func validIdentifier(value string, maximum int) bool {
	return len(value) != 0 && len(value) <= maximum && identifierPattern.MatchString(value)
}

func validEventName(value string) bool {
	return len(value) != 0 && len(value) <= maximumEventNameBytes && eventPattern.MatchString(value)
}

type slogAdapter struct {
	logger *Logger
	groups []string
	attrs  []slog.Attr
}

func (adapter *slogAdapter) Enabled(ctx context.Context, level slog.Level) bool {
	return adapter.logger.controller.enabledValue(adapter.logger.scope, int(level)) && adapter.logger.handler.Enabled(ctx, level)
}

func (adapter *slogAdapter) Handle(ctx context.Context, record slog.Record) error {
	adapter.logger.stats.attempted.Add(1)
	canonical := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	canonical.AddAttrs(slog.String("schema", "spice.log/v1"), slog.String("event", "slog.record"), slog.String("application", adapter.logger.application))
	if adapter.logger.scope.Module != "" {
		canonical.AddAttrs(slog.String("module", adapter.logger.scope.Module))
	}
	if adapter.logger.scope.Component != "" {
		canonical.AddAttrs(slog.String("component", adapter.logger.scope.Component))
	}
	attributes := append([]slog.Attr(nil), adapter.attrs...)
	record.Attrs(func(attribute slog.Attr) bool { attributes = append(attributes, attribute); return true })
	if len(adapter.groups) != 0 {
		for _, group := range slices.Backward(adapter.groups) {
			attributes = []slog.Attr{slog.Group(group, attrsToAny(attributes)...)}
		}
	}
	if len(attributes) != 0 {
		canonical.AddAttrs(slog.Group("attributes", attrsToAny(attributes)...))
	}
	if err := handleRecord(adapter.logger.handler, ctx, canonical); err != nil {
		adapter.logger.stats.failed.Add(1)
		return err
	}
	adapter.logger.stats.emitted.Add(1)
	return nil
}

func (adapter *slogAdapter) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *adapter
	clone.groups = append([]string(nil), adapter.groups...)
	clone.attrs = append(append([]slog.Attr(nil), adapter.attrs...), attrs...)
	return &clone
}

func (adapter *slogAdapter) WithGroup(name string) slog.Handler {
	clone := *adapter
	clone.groups = append(append([]string(nil), adapter.groups...), name)
	clone.attrs = append([]slog.Attr(nil), adapter.attrs...)
	return &clone
}

func attrsToAny(attrs []slog.Attr) []any {
	values := make([]any, len(attrs))
	for index, attr := range attrs {
		values[index] = attr
	}
	return values
}
