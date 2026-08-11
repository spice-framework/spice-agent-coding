package logging

import (
	"context"
	"errors"
	"strings"
)

// ErrorDetails is explicitly reviewed, bounded information safe for logs.
type ErrorDetails struct {
	Kind    string
	Code    string
	Message string
}

// SafeError opts one error into reviewed structured details. Implementations
// must never return secrets, user content, raw rejected input, or stack traces.
type SafeError interface {
	error
	SafeLogError() ErrorDetails
}

// ClassifyError converts an error into safe fixed details.
func ClassifyError(err error) ErrorDetails {
	if err == nil {
		return ErrorDetails{}
	}
	if errors.Is(err, context.Canceled) {
		return ErrorDetails{Kind: "cancelled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorDetails{Kind: "deadline_exceeded"}
	}
	if safe, found := errors.AsType[SafeError](err); found {
		details := safeErrorDetails(safe)
		if validIdentifier(details.Kind, maximumFieldKeyBytes) &&
			(details.Code == "" || validEventName(details.Code)) &&
			len(details.Message) <= maximumSafeErrorMessageBytes &&
			!strings.ContainsRune(details.Message, '\x00') {
			return details
		}
	}
	return ErrorDetails{Kind: "internal"}
}

func safeErrorDetails(safe SafeError) (details ErrorDetails) {
	defer func() {
		if recover() != nil {
			details = ErrorDetails{}
		}
	}()
	return safe.SafeLogError()
}

// ErrorFields returns bounded fields for automatic observers.
func ErrorFields(err error) []Field {
	details := ClassifyError(err)
	if details.Kind == "" {
		return nil
	}
	fields := []Field{String("error_kind", details.Kind)}
	if details.Code != "" {
		fields = append(fields, String("error_code", details.Code))
	}
	if details.Message != "" {
		fields = append(fields, String("error_message", details.Message))
	}
	return fields
}
