package architectureproof

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// ResponsesFixture is a local deterministic Responses-compatible endpoint.
// It records only safe protocol facts and never retains authorization values.
type ResponsesFixture struct {
	mu                sync.Mutex
	server            *httptest.Server
	requests          int
	authorized        bool
	continuation      bool
	protocolViolation string
	cancelMode        bool
	requestStarted    chan struct{}
	providerCanceled  bool
}

func (fixture *ResponsesFixture) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.requests++
	authorized := request.Header.Get("Authorization") == "Bearer "+fixtureSecret
	if fixture.requests == 1 {
		fixture.authorized = authorized
	} else {
		fixture.authorized = fixture.authorized && authorized
	}
	if request.URL.Path != "/v1/responses" || request.Method != http.MethodPost {
		fixture.fail(writer, "unexpected request target")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20)).Decode(&body); err != nil {
		fixture.fail(writer, "invalid request JSON")
		return
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		fixture.fail(writer, "cannot inspect request JSON")
		return
	}
	text := string(encoded)
	if strings.Contains(text, fixtureSecret) {
		fixture.fail(writer, "credential leaked into request body")
		return
	}
	if fixture.cancelMode {
		close(fixture.requestStarted)
		<-request.Context().Done()
		fixture.providerCanceled = true
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	switch fixture.requests {
	case 1:
		if !strings.Contains(text, `"name":"read"`) {
			fixture.fail(writer, "compiled read tool was not declared")
			return
		}
		fixture.writeEvents(
			writer,
			`{"type":"response.completed","sequence_number":1,"response":{"id":"proof-1","model":"proof-model","status":"completed","service_tier":"default","usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6},"output":[{"type":"function_call","id":"item-read","call_id":"call-read","name":"read","arguments":"{\"path\":\"README.md\"}","status":"completed"}]}}`,
		)
	case 2:
		fixture.continuation = strings.Contains(text, `"type":"function_call_output"`) &&
			strings.Contains(text, "Spice Agent architecture proof")
		if !fixture.continuation {
			fixture.fail(writer, "tool result was not preserved in continuation")
			return
		}
		fixture.writeEvents(
			writer,
			`{"type":"response.output_text.delta","sequence_number":1,"item_id":"item-final","output_index":0,"content_index":0,"delta":"architecture proof complete"}`,
			`{"type":"response.completed","sequence_number":2,"response":{"id":"proof-2","model":"proof-model","status":"completed","service_tier":"default","usage":{"input_tokens":8,"output_tokens":3,"total_tokens":11},"output":[{"type":"message","id":"item-final","role":"assistant","status":"completed","content":[{"type":"output_text","text":"architecture proof complete","annotations":[]}] }]}}`,
		)
	default:
		fixture.fail(writer, "unexpected extra provider request")
	}
}

func (fixture *ResponsesFixture) writeEvents(writer io.Writer, values ...string) {
	for _, value := range values {
		if _, err := fmt.Fprintf(writer, "data: %s\n\n", value); err != nil {
			fixture.protocolViolation = "write streaming response"
			return
		}
	}
	if _, err := io.WriteString(writer, "data: [DONE]\n\n"); err != nil {
		fixture.protocolViolation = "finish streaming response"
	}
}

func (fixture *ResponsesFixture) fail(writer http.ResponseWriter, message string) {
	fixture.protocolViolation = message
	http.Error(writer, message, http.StatusBadRequest)
}

func (fixture *ResponsesFixture) snapshot() (int, bool, bool, string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.requests, fixture.authorized, fixture.continuation, fixture.protocolViolation
}

func (fixture *ResponsesFixture) prepareCancellation() (<-chan struct{}, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.requests != 0 || fixture.cancelMode {
		return nil, fmt.Errorf("responses fixture already started")
	}
	fixture.cancelMode = true
	fixture.requestStarted = make(chan struct{})
	return fixture.requestStarted, nil
}

func (fixture *ResponsesFixture) cancellationObserved() bool {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.providerCanceled
}
