package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
)

type opencodeEventCapture struct {
	mu           sync.Mutex
	cancel       context.CancelFunc
	maximumTools int
	maximumSteps int
	raw          []byte
	pending      []byte
	summary      opencodeEventSummary
}

func (capture *opencodeEventCapture) newOpenCodeEventCapture(cancel context.CancelFunc, maximumTools, maximumSteps int) *opencodeEventCapture {
	return &opencodeEventCapture{cancel: cancel, maximumTools: maximumTools, maximumSteps: maximumSteps}
}

func (capture *opencodeEventCapture) Write(value []byte) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.summary.SafetyFailure != "" {
		return len(value), nil
	}
	if len(capture.raw)+len(value) > maximumOpenCodeEventBytes {
		capture.fail("event output cap exceeded")
		return len(value), nil
	}
	capture.raw = append(capture.raw, value...)
	capture.pending = append(capture.pending, value...)
	for {
		separator := bytes.IndexByte(capture.pending, '\n')
		if separator < 0 {
			break
		}
		line := bytes.TrimSpace(capture.pending[:separator])
		capture.pending = capture.pending[separator+1:]
		if len(line) == 0 {
			continue
		}
		if err := capture.consume(line); err != nil {
			capture.fail("invalid OpenCode event stream")
			break
		}
	}
	return len(value), nil
}

func (capture *opencodeEventCapture) Summary() opencodeEventSummary {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(bytes.TrimSpace(capture.pending)) != 0 && capture.summary.SafetyFailure == "" {
		if err := capture.consume(bytes.TrimSpace(capture.pending)); err != nil {
			capture.summary.SafetyFailure = "invalid trailing OpenCode event"
		}
	}
	result := capture.summary
	result.Tools = slices.Clone(capture.summary.Tools)
	return result
}

func (capture *opencodeEventCapture) consume(line []byte) error {
	var event opencodeEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return err
	}
	switch event.Type {
	case "step_start":
		return nil
	case "step_finish":
		return capture.consumeStep(event.Part)
	case "tool_use":
		return capture.consumeTool(event.Part)
	case "text":
		return capture.consumeText(event.Part)
	case "error":
		capture.summary.ErrorClass = (&opencodeEventCapture{}).classifyOpenCodeError(event.Error)
	case "reasoning":
		return errors.New("reasoning output was not requested")
	default:
		return errors.New("unknown OpenCode event type")
	}
	return nil
}

func (capture *opencodeEventCapture) consumeStep(part opencodeEventPart) error {
	if part.Type != "step-finish" || math.IsNaN(part.Cost) || math.IsInf(part.Cost, 0) || part.Cost < 0 ||
		math.IsNaN(part.Tokens.Output) || math.IsInf(part.Tokens.Output, 0) ||
		part.Tokens.Output < 0 || part.Tokens.Output > maximumOpenCodeOutputTokens {
		return errors.New("invalid OpenCode step accounting")
	}
	capture.summary.Cost += part.Cost
	capture.summary.Steps++
	if capture.summary.Cost != 0 || capture.summary.Steps > capture.maximumSteps {
		capture.fail("cost or step cap exceeded")
	}
	return nil
}

func (capture *opencodeEventCapture) consumeTool(part opencodeEventPart) error {
	if part.Type != "tool" || part.Tool == "" {
		return errors.New("invalid OpenCode tool event")
	}
	capture.summary.Tools = append(capture.summary.Tools, part.Tool)
	if len(capture.summary.Tools) > capture.maximumTools {
		capture.fail("tool cap exceeded")
	}
	return nil
}

func (capture *opencodeEventCapture) consumeText(part opencodeEventPart) error {
	if part.Type != "text" {
		return errors.New("invalid OpenCode text event")
	}
	if len(capture.summary.Text)+len(part.Text) > maximumOpenCodeDiagnosticBytes {
		capture.fail("model text cap exceeded")
		return nil
	}
	capture.summary.Text += part.Text
	return nil
}

func (capture *opencodeEventCapture) fail(reason string) {
	if capture.summary.SafetyFailure == "" {
		capture.summary.SafetyFailure = reason
		capture.cancel()
	}
}

func (capture *opencodeEventCapture) classifyOpenCodeError(value opencodeEventError) string {
	message := strings.ToLower(value.Name + " " + value.Data.Message)
	switch {
	case value.Data.StatusCode == http.StatusTooManyRequests || strings.Contains(message, "rate limit") || strings.Contains(message, "too many requests"):
		return "rate-limited"
	case value.Data.StatusCode == http.StatusUnauthorized || value.Data.StatusCode == http.StatusForbidden || strings.Contains(message, "auth") || strings.Contains(message, "credential"):
		return "infrastructure-auth"
	default:
		return "infrastructure-model"
	}
}
