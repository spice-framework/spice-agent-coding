// Package observability provides instance-owned contracts and adapters for
// Spice's dependency-free lifecycle, HTTP, and generated method observations.
package observability

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/spice-framework/spice/lifecycle"
	"github.com/spice-framework/spice/web"
)

const (
	httpMessage      = "Spice HTTP request completed"
	lifecycleMessage = "Spice lifecycle callback"
)

// SlogHTTPObserver writes one bounded, route-template-based completion record
// for each generated HTTP request. It never records raw URLs, headers, bodies,
// query values, or client-provided path values.
type SlogHTTPObserver struct {
	logger *slog.Logger
}

// NewSlogHTTPObserver constructs an instance-owned structured HTTP observer.
func NewSlogHTTPObserver(logger *slog.Logger) (*SlogHTTPObserver, error) {
	if logger == nil {
		return nil, errors.New("construct slog HTTP observer: logger is nil")
	}
	return &SlogHTTPObserver{logger: logger}, nil
}

// BeginHTTP implements web.HTTPObserver.
func (observer *SlogHTTPObserver) BeginHTTP(
	ctx context.Context,
	route web.RouteMetadata,
) (context.Context, func(web.HTTPResult)) {
	if observer == nil || observer.logger == nil {
		return ctx, nil
	}
	return ctx, func(result web.HTTPResult) {
		observer.logger.LogAttrs(
			ctx,
			httpLevel(result),
			httpMessage,
			slog.String("event", "http.server.request"),
			slog.String("route_id", route.ID),
			slog.String("module", route.Module),
			slog.String("http_method", route.Method),
			slog.String("http_route", route.Pattern),
			slog.Int("http_status", result.Status),
			slog.Int64("response_bytes", result.Bytes),
			slog.Int64("duration_ns", result.Duration.Nanoseconds()),
			slog.Bool("panicked", result.Panicked),
		)
	}
}

func httpLevel(result web.HTTPResult) slog.Level {
	switch {
	case result.Panicked || result.Status >= http.StatusInternalServerError:
		return slog.LevelError
	case result.Status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// NewSlogLifecycleObserver constructs a structured lifecycle observer. Begin
// records use debug level; successful completion uses info and failures use
// error. Component and module values come from compiler-generated metadata.
func NewSlogLifecycleObserver(logger *slog.Logger) (lifecycle.Observer, error) {
	if logger == nil {
		return nil, errors.New("construct slog lifecycle observer: logger is nil")
	}
	return func(ctx context.Context, observation lifecycle.Observation) {
		level := slog.LevelInfo
		if observation.Phase == lifecycle.PhaseBegin {
			level = slog.LevelDebug
		}
		if observation.Err != nil {
			level = slog.LevelError
		}
		attributes := []slog.Attr{
			slog.String("event", "application.lifecycle"),
			slog.String("module", observation.Module),
			slog.String("component", observation.Component),
			slog.String("operation", string(observation.Operation)),
			slog.String("phase", string(observation.Phase)),
		}
		if observation.Err != nil {
			attributes = append(attributes, slog.Any("error", observation.Err))
		}
		logger.LogAttrs(ctx, level, lifecycleMessage, attributes...)
	}, nil
}

var _ web.HTTPObserver = (*SlogHTTPObserver)(nil)
