package observability

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/spice-framework/spice/async"
	"github.com/spice-framework/spice/batch"
	"github.com/spice-framework/spice/cache"
	"github.com/spice-framework/spice/data"
	spiceevent "github.com/spice-framework/spice/event"
	"github.com/spice-framework/spice/event/outbox"
	"github.com/spice-framework/spice/lifecycle"
	"github.com/spice-framework/spice/logging"
	"github.com/spice-framework/spice/mail/mailtest"
	"github.com/spice-framework/spice/migration"
	"github.com/spice-framework/spice/retry"
	"github.com/spice-framework/spice/schedule"
	"github.com/spice-framework/spice/security"
	"github.com/spice-framework/spice/web"
)

// LoggingObservers contains one safe adapter for every public Spice
// observation seam. Generated code selects only the fields it needs.
type LoggingObservers struct {
	Lifecycle     lifecycle.Observer
	HTTP          web.HTTPObserver
	Method        MethodObserver
	Authorization security.Observer
	Schedule      schedule.Observer
	Async         async.Observer
	Retry         retry.Observer
	Cache         cache.Observer
	Event         spiceevent.Observer
	Transaction   data.Observer
	Batch         batch.Observer
	Outbox        outbox.Observer
	Migration     migration.Observer
	Mail          mailtest.Observer
}

// NewLoggingObservers constructs instance-owned, payload-free adapters.
func NewLoggingObservers(logger *logging.Logger) (*LoggingObservers, error) {
	if logger == nil {
		return nil, errors.New("construct logging observers: logger is nil")
	}
	adapter := &loggingAdapter{logger: logger}
	return &LoggingObservers{
		Lifecycle:     adapter.lifecycle,
		HTTP:          web.HTTPObserverFunc(adapter.http),
		Method:        adapter,
		Authorization: adapter.authorization,
		Schedule:      adapter.schedule,
		Async:         adapter.async,
		Retry:         adapter.retry,
		Cache:         adapter.cache,
		Event:         adapter,
		Transaction:   adapter,
		Batch:         adapter.batch,
		Outbox:        adapter.outbox,
		Migration:     adapter.migration,
		Mail:          adapter.mail,
	}, nil
}

type loggingAdapter struct{ logger *logging.Logger }

func (adapter *loggingAdapter) emit(
	ctx context.Context,
	level logging.Level,
	event string,
	message string,
	scope logging.Scope,
	fields ...logging.Field,
) {
	if err := adapter.logger.Emit(ctx, logging.Record{
		Level: level, Event: event, Message: message, Scope: scope, Fields: fields,
	}); err != nil {
		return
	}
}

func (adapter *loggingAdapter) lifecycle(ctx context.Context, observation lifecycle.Observation) {
	level := logging.LevelDebug
	if observation.Phase == lifecycle.PhaseEnd {
		level = completionLevel(observation.Err, false)
	}
	fields := []logging.Field{
		logging.String("operation", string(observation.Operation)),
		logging.String("phase", string(observation.Phase)),
	}
	fields = append(fields, logging.ErrorFields(observation.Err)...)
	adapter.emit(ctx, level, "application.lifecycle", "Spice lifecycle callback",
		logging.Scope{Module: observation.Module, Component: observation.Component}, fields...)
}

func (adapter *loggingAdapter) http(
	ctx context.Context,
	route web.RouteMetadata,
) (context.Context, func(web.HTTPResult)) {
	return ctx, func(result web.HTTPResult) {
		level := logging.LevelInfo
		if result.Panicked || result.Status >= http.StatusInternalServerError {
			level = logging.LevelError
		} else if result.Status >= http.StatusBadRequest {
			level = logging.LevelWarn
		}
		adapter.emit(
			ctx, level, "http.server.request", "Spice HTTP request completed",
			logging.Scope{Module: route.Module},
			logging.String("route_id", route.ID),
			logging.String("http_method", route.Method),
			logging.String("http_route", route.Pattern),
			logging.Int64("http_status", int64(result.Status)),
			logging.Int64("response_bytes", result.Bytes),
			logging.Int64("duration_ns", result.Duration.Nanoseconds()),
			logging.Bool("panicked", result.Panicked),
		)
	}
}

func (adapter *loggingAdapter) BeginMethod(
	ctx context.Context,
	definition MethodDefinition,
) (context.Context, func(MethodResult)) {
	adapter.emit(ctx, logging.LevelDebug, "method.invocation", "Spice method invocation started",
		logging.Scope{Module: definition.Module},
		logging.String("method_id", definition.ID), logging.String("service", definition.Service),
		logging.String("method", definition.Method), logging.String("phase", "begin"))
	return ctx, func(result MethodResult) {
		fields := []logging.Field{
			logging.String("method_id", result.Definition.ID),
			logging.String("service", result.Definition.Service),
			logging.String("method", result.Definition.Method),
			logging.String("phase", "end"),
			logging.Int64("duration_ns", result.Duration.Nanoseconds()),
			logging.Bool("panicked", result.Panicked),
		}
		fields = append(fields, logging.ErrorFields(result.Err)...)
		adapter.emit(ctx, completionLevel(result.Err, result.Panicked), "method.invocation",
			"Spice method invocation completed", logging.Scope{Module: result.Definition.Module}, fields...)
	}
}

func (adapter *loggingAdapter) authorization(ctx context.Context, decision security.Decision) {
	level := logging.LevelInfo
	if !decision.Allowed {
		level = logging.LevelWarn
	}
	adapter.emit(ctx, level, "security.authorization", "Spice authorization decision completed",
		logging.Scope{Module: decision.Definition.Module},
		logging.String("policy_id", decision.Definition.ID),
		logging.Bool("allowed", decision.Allowed),
		logging.String("reason", string(decision.Reason)),
		logging.Int64("duration_ns", decision.Duration.Nanoseconds()))
}

func (adapter *loggingAdapter) schedule(ctx context.Context, result schedule.Result) {
	fields := []logging.Field{
		logging.String("job_id", result.Definition.ID), logging.Uint64("run", result.Run),
		logging.Int64("duration_ns", result.Duration.Nanoseconds()), logging.Bool("panicked", result.Panicked),
	}
	fields = append(fields, logging.ErrorFields(result.Err)...)
	adapter.emit(ctx, completionLevel(result.Err, result.Panicked), "schedule.job",
		"Spice scheduled job completed", logging.Scope{Module: result.Definition.Module}, fields...)
}

func (adapter *loggingAdapter) async(ctx context.Context, result async.Result) {
	fields := []logging.Field{
		logging.String("task_id", result.Definition.ID), logging.Int64("duration_ns", result.Duration.Nanoseconds()),
		logging.Bool("panicked", result.Panicked),
	}
	fields = append(fields, logging.ErrorFields(result.Err)...)
	adapter.emit(ctx, completionLevel(result.Err, result.Panicked), "async.task",
		"Spice asynchronous task completed", logging.Scope{Module: result.Definition.Module}, fields...)
}

func (adapter *loggingAdapter) retry(ctx context.Context, observation retry.Observation) {
	level := logging.LevelInfo
	if observation.Panicked || observation.Err != nil && observation.Attempt.Number == observation.Attempt.Max {
		level = logging.LevelError
	} else if observation.Err != nil {
		level = logging.LevelWarn
	}
	fields := []logging.Field{
		logging.String("retry_id", observation.ID),
		logging.Int64("attempt", int64(observation.Attempt.Number)),
		logging.Int64("max_attempts", int64(observation.Attempt.Max)),
		logging.Int64("duration_ns", observation.Duration.Nanoseconds()),
		logging.Int64("next_backoff_ns", observation.NextBackoff.Nanoseconds()),
		logging.Bool("panicked", observation.Panicked),
	}
	fields = append(fields, logging.ErrorFields(observation.Err)...)
	adapter.emit(ctx, level, "retry.attempt", "Spice retry attempt completed",
		logging.Scope{Module: observation.Module}, fields...)
}

func (adapter *loggingAdapter) cache(ctx context.Context, observation cache.Observation) {
	adapter.emit(ctx, logging.LevelDebug, "cache.operation", "Spice cache operation completed",
		logging.Scope{Module: observation.Definition.Module},
		logging.String("cache_id", observation.Definition.ID),
		logging.String("operation", string(observation.Operation)),
		logging.Int64("duration_ns", observation.Duration.Nanoseconds()),
		logging.Bool("hit", observation.Hit), logging.Int64("evicted", int64(observation.Evicted)),
		logging.Int64("removed", int64(observation.Removed)), logging.Int64("size", int64(observation.Size)))
}

func (adapter *loggingAdapter) BeginEvent(
	ctx context.Context,
	interaction spiceevent.Interaction,
) (context.Context, func(spiceevent.Result)) {
	fields := eventFields(interaction)
	fields = append(fields, logging.String("phase", "begin"))
	adapter.emit(ctx, logging.LevelDebug, "event.delivery", "Spice event delivery started",
		logging.Scope{Module: interaction.Event.Module}, fields...)
	return ctx, func(result spiceevent.Result) {
		fields := eventFields(result.Interaction)
		fields = append(fields, logging.String("phase", "end"),
			logging.Int64("duration_ns", result.Duration.Nanoseconds()), logging.Bool("panicked", result.Panicked))
		fields = append(fields, logging.ErrorFields(result.Err)...)
		adapter.emit(ctx, completionLevel(result.Err, result.Panicked), "event.delivery",
			"Spice event delivery completed", logging.Scope{Module: result.Interaction.Event.Module}, fields...)
	}
}

func eventFields(interaction spiceevent.Interaction) []logging.Field {
	return []logging.Field{
		logging.String("event_id", interaction.Event.ID),
		logging.String("publisher_module", interaction.Event.Module),
		logging.String("subscriber_id", interaction.Subscriber.ID),
		logging.String("subscriber_module", interaction.Subscriber.Module),
		logging.Int64("subscriber_order", int64(interaction.Subscriber.Order)),
	}
}

func (adapter *loggingAdapter) BeginTransaction(
	ctx context.Context,
	definition data.Definition,
) (context.Context, func(data.Result)) {
	adapter.emit(ctx, logging.LevelDebug, "data.transaction", "Spice transaction started",
		logging.Scope{Module: definition.Module}, transactionFields(definition, "begin")...)
	return ctx, func(result data.Result) {
		fields := transactionFields(result.Definition, "end")
		fields = append(fields, logging.Int64("duration_ns", result.Duration.Nanoseconds()),
			logging.Bool("panicked", result.Panicked))
		fields = append(fields, logging.ErrorFields(result.Err)...)
		adapter.emit(ctx, completionLevel(result.Err, result.Panicked), "data.transaction",
			"Spice transaction completed", logging.Scope{Module: result.Definition.Module}, fields...)
	}
}

func transactionFields(definition data.Definition, phase string) []logging.Field {
	isolation := definition.Isolation.String()
	if definition.Isolation == sql.LevelDefault {
		isolation = "default"
	}
	return []logging.Field{
		logging.String("transaction_id", definition.ID), logging.String("isolation", isolation),
		logging.Bool("read_only", definition.ReadOnly), logging.String("phase", phase),
	}
}

func (adapter *loggingAdapter) batch(ctx context.Context, observation batch.Observation) {
	fields := []logging.Field{
		logging.String("batch_id", observation.Definition.ID), logging.String("operation", string(observation.Operation)),
		logging.String("step", observation.Step), logging.Uint64("attempt", observation.Attempt),
		logging.Int64("duration_ns", observation.Duration.Nanoseconds()), logging.Bool("resumed", observation.Resumed),
		logging.Bool("completed", observation.Completed), logging.Bool("panicked", observation.Panicked),
	}
	fields = append(fields, logging.ErrorFields(observation.Err)...)
	adapter.emit(ctx, completionLevel(observation.Err, observation.Panicked), "batch.operation",
		"Spice batch operation completed", logging.Scope{Module: observation.Definition.Module}, fields...)
}

func (adapter *loggingAdapter) outbox(ctx context.Context, observation outbox.Observation) {
	fields := []logging.Field{
		logging.String("topic", observation.Topic), logging.Int64("attempt", int64(observation.Attempt)),
		logging.Int64("duration_ns", observation.Duration.Nanoseconds()), logging.Bool("published", observation.Published),
		logging.Bool("completed", observation.Completed), logging.Bool("released", observation.Released),
		logging.Bool("panicked", observation.Panicked),
	}
	fields = append(fields, logging.ErrorFields(observation.Err)...)
	adapter.emit(ctx, completionLevel(observation.Err, observation.Panicked), "outbox.delivery",
		"Spice outbox delivery completed", logging.Scope{Module: observation.Module}, fields...)
}

func (adapter *loggingAdapter) migration(ctx context.Context, observation migration.Observation) {
	fields := []logging.Field{
		logging.Uint64("version", observation.Version), logging.String("migration", observation.Name),
		logging.Int64("duration_ns", observation.Duration.Nanoseconds()),
	}
	fields = append(fields, logging.ErrorFields(observation.Err)...)
	adapter.emit(ctx, completionLevel(observation.Err, false), "migration.execution",
		"Spice migration completed", logging.Scope{Module: observation.Module}, fields...)
}

func (adapter *loggingAdapter) mail(ctx context.Context, observation mailtest.Observation) {
	level := logging.LevelInfo
	if observation.Outcome == mailtest.OutcomeFailed || observation.Outcome == mailtest.OutcomeRejected {
		level = logging.LevelWarn
	}
	adapter.emit(ctx, level, "mail.delivery", "Spice mail delivery completed", logging.Scope{},
		logging.Uint64("attempt", observation.Attempt), logging.String("message_id", observation.MessageID),
		logging.String("outcome", string(observation.Outcome)))
}

func completionLevel(err error, panicked bool) logging.Level {
	if panicked {
		return logging.LevelError
	}
	if err == nil {
		return logging.LevelInfo
	}
	details := logging.ClassifyError(err)
	if details.Kind == "cancelled" || details.Kind == "deadline_exceeded" {
		return logging.LevelWarn
	}
	return logging.LevelError
}

var (
	_ MethodObserver      = (*loggingAdapter)(nil)
	_ spiceevent.Observer = (*loggingAdapter)(nil)
	_ data.Observer       = (*loggingAdapter)(nil)
)
