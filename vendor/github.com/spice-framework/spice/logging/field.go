package logging

import (
	"log/slog"
	"time"
)

// Field is one typed attribute in a safe Spice record. Its representation is
// opaque so arbitrary values cannot bypass record validation.
type Field struct{ attribute slog.Attr }

// String constructs one bounded string field.
func String(key, value string) Field { return Field{attribute: slog.String(key, value)} }

// Bool constructs one Boolean field.
func Bool(key string, value bool) Field { return Field{attribute: slog.Bool(key, value)} }

// Int64 constructs one signed integer field.
func Int64(key string, value int64) Field { return Field{attribute: slog.Int64(key, value)} }

// Uint64 constructs one unsigned integer field.
func Uint64(key string, value uint64) Field { return Field{attribute: slog.Uint64(key, value)} }

// Float64 constructs one floating-point field.
func Float64(key string, value float64) Field { return Field{attribute: slog.Float64(key, value)} }

// Duration constructs one duration field encoded as nanoseconds.
func Duration(key string, value time.Duration) Field {
	return Field{attribute: slog.Duration(key, value)}
}

// Time constructs one timestamp field normalized by the selected handler.
func Time(key string, value time.Time) Field { return Field{attribute: slog.Time(key, value)} }
