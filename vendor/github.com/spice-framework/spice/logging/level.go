// Package logging provides Spice-native, instance-owned structured logging.
package logging

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// Level is the severity of one structured record.
type Level int8

const (
	// LevelTrace enables the most detailed diagnostic records.
	LevelTrace Level = -8
	// LevelDebug enables developer diagnostic records.
	LevelDebug Level = -4
	// LevelInfo enables ordinary operational records.
	LevelInfo Level = 0
	// LevelWarn enables degraded or rejected operation records.
	LevelWarn Level = 4
	// LevelError enables failed operation records.
	LevelError Level = 8
	// LevelOff disables every record in a configured scope.
	LevelOff Level = 127

	// Trace is the canonical trace severity.
	Trace = LevelTrace
	// Debug is the canonical debug severity.
	Debug = LevelDebug
	// Info is the canonical informational severity.
	Info = LevelInfo
	// Warn is the canonical warning severity.
	Warn = LevelWarn
	// Error is the canonical error severity.
	Error = LevelError
	// Off disables every record in a configured scope.
	Off = LevelOff
)

// ParseLevel decodes a case-insensitive canonical level name.
func ParseLevel(value string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return LevelDebug, nil
	case "info":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	case "off":
		return LevelOff, nil
	default:
		return 0, fmt.Errorf("logging level %q is unsupported", value)
	}
}

// String returns the uppercase canonical level name.
func (level Level) String() string {
	switch level {
	case LevelTrace:
		return "TRACE"
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	case LevelOff:
		return "OFF"
	default:
		return fmt.Sprintf("LEVEL(%d)", level)
	}
}

func (level Level) validRecordLevel() bool {
	return level == LevelTrace || level == LevelDebug || level == LevelInfo ||
		level == LevelWarn || level == LevelError
}

func (level Level) validConfiguredLevel() bool {
	return level.validRecordLevel() || level == LevelOff
}

func (level Level) slog() slog.Level { return slog.Level(level) }

// MarshalJSON emits the canonical name used by management responses.
func (level Level) MarshalJSON() ([]byte, error) {
	if !level.validConfiguredLevel() {
		return nil, fmt.Errorf("marshal logging level: level %d is invalid", level)
	}
	return json.Marshal(level.String())
}
