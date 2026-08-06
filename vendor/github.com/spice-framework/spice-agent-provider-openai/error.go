package openaiprovider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3/responses"
)

const (
	maxErrorCodeBytes = 128
	maxRequestIDBytes = 256
)

// Error is bounded, redacted OpenAI failure metadata. It deliberately excludes
// response bodies, prompts, tool data, request URLs, and authorization headers.
type Error struct {
	code       string
	statusCode int
	requestID  string
	retryable  bool
	cause      error
}

// Error returns diagnostic-safe failure text.
func (failure *Error) Error() string {
	if failure == nil {
		return "OpenAI provider failure"
	}
	if failure.statusCode != 0 {
		return fmt.Sprintf("OpenAI provider %s (HTTP %d)", failure.code, failure.statusCode)
	}
	return "OpenAI provider " + failure.code
}

// Unwrap preserves only context cancellation and deadline causes. Upstream SDK
// errors are intentionally not exposed because they can include request URLs or
// response bodies.
func (failure *Error) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

// Code returns the stable provider-owned category.
func (failure *Error) Code() string {
	if failure == nil {
		return ""
	}
	return failure.code
}

// StatusCode returns an HTTP status, or zero when no response was received.
func (failure *Error) StatusCode() int {
	if failure == nil {
		return 0
	}
	return failure.statusCode
}

// RequestID returns the bounded OpenAI request identity when supplied.
func (failure *Error) RequestID() string {
	if failure == nil {
		return ""
	}
	return failure.requestID
}

// Retryable reports whether a failure happened before an observed stream and
// is safe to retry with the operation's stable idempotency key.
func (failure *Error) Retryable() bool { return failure != nil && failure.retryable }

func normalizeFailure(err error, retryablePhase bool) *Error {
	if err == nil {
		return &Error{code: "stream_ended"}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{code: "cancelled", cause: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{code: "deadline_exceeded", cause: context.DeadlineExceeded}
	}
	if apiError, ok := errors.AsType[*responses.Error](err); ok {
		code := safeCode(apiError.Code)
		if code == "provider_error" {
			code = statusCode(apiError.StatusCode)
		}
		return &Error{
			code:       code,
			statusCode: apiError.StatusCode,
			requestID:  responseRequestID(apiError.Response),
			retryable:  retryablePhase && retryableStatus(apiError.StatusCode),
		}
	}
	return &Error{code: "transport_error"}
}

func safeCode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > maxErrorCodeBytes {
		return "provider_error"
	}
	for index := range len(value) {
		character := value[index]
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') &&
			character != '_' && character != '-' && character != '.' {
			return "provider_error"
		}
	}
	return value
}

func statusCode(status int) string {
	if status == 0 {
		return "provider_error"
	}
	return fmt.Sprintf("http_%d", status)
}

func responseRequestID(response *http.Response) string {
	if response == nil {
		return ""
	}
	value := strings.TrimSpace(response.Header.Get("x-request-id"))
	if value == "" || len(value) > maxRequestIDBytes {
		return ""
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return ""
		}
	}
	return value
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusConflict ||
		status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}
