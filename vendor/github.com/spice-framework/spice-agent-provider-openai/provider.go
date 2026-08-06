package openaiprovider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/spice-framework/spice-agent/message"
	"github.com/spice-framework/spice-agent/model"
	"github.com/spice-framework/spice-agent/tool"
)

const (
	maxTranslatedRequestBytes = 32 << 20
	maxTranslatedInputItems   = 8192
	maxResponseOutputItems    = 4096
	translatedTextChunkBytes  = 64 << 10
	maxMetadataFactBytes      = 512
)

// MetadataNamespace is the exact provider extension namespace applications
// may allowlist in agent.EngineOptions. Its values never contain content,
// credentials, headers, URLs, or raw provider payloads.
const MetadataNamespace = "github.com/spice-framework/spice-agent-provider-openai"

var _ model.Provider = (*Client)(nil)

// Stream translates one provider-neutral request into an OpenAI Responses
// stream. Request.Model is authoritative; configuration never overrides it.
// The returned stream supports one Recv caller racing with Close. Concurrent
// Recv calls are outside the model.Stream contract.
func (client *Client) Stream(ctx context.Context, request model.Request) (model.Stream, error) {
	if client == nil || client.start == nil {
		return nil, providerStartError("provider_unavailable", "OpenAI provider is unavailable", false, nil)
	}
	if ctx == nil {
		return nil, providerStartError("invalid_request", "OpenAI request context is nil", false, nil)
	}
	params, allowedTools, err := translateRequest(request)
	if err != nil {
		return nil, providerStartError("invalid_request", "OpenAI request translation failed", false, err)
	}
	operationContext, cancel := context.WithCancel(ctx)
	source := client.start(operationContext, params, option.WithHeader("Idempotency-Key", operationKey(request.OperationID())))
	if source == nil {
		cancel()
		return nil, providerStartError("provider_unavailable", "OpenAI stream was not created", false, nil)
	}
	if !source.Next() {
		sourceErr := source.Err()
		operationErr := operationContext.Err()
		cancel()
		if closeErr := source.Close(); sourceErr == nil {
			sourceErr = closeErr
		}
		if operationErr != nil {
			sourceErr = operationErr
		}
		failure := normalizeFailure(sourceErr, true)
		return nil, providerStartError(failure.code, failure.Error(), failure.retryable, failure)
	}
	first := source.Current()
	result := &stream{
		operationDone: operationContext.Done(),
		operationErr:  operationContext.Err,
		cancel:        cancel,
		source:        source,
		buffered:      &first,
		raw:           make(chan rawResult, 1),
		pumpDone:      make(chan struct{}),
		allowedTools:  allowedTools,
		seenCalls:     make(map[string]struct{}),
	}
	go result.pump()
	return result, nil
}

func providerStartError(code, message string, retryable bool, cause error) error {
	var metadata []model.Metadata
	if failure, ok := errors.AsType[*Error](cause); ok {
		translated, metadataErr := errorMetadata(failure)
		if metadataErr != nil {
			return metadataErr
		}
		metadata = translated
	}
	problem, err := model.NewProblem(code, message, retryable, metadata...)
	if err != nil {
		return fmt.Errorf("construct OpenAI provider problem: %w", err)
	}
	failure, err := model.NewProviderError(problem, cause)
	if err != nil {
		return fmt.Errorf("construct OpenAI provider error: %w", err)
	}
	return failure
}

func operationKey(operationID model.OperationID) string {
	digest := sha256.Sum256([]byte(operationID))
	return "spice-" + hex.EncodeToString(digest[:])
}

func translateRequest(request model.Request) (responses.ResponseNewParams, map[string]struct{}, error) {
	if strings.TrimSpace(request.Model()) == "" {
		return responses.ResponseNewParams{}, nil, errors.New("model selection is empty")
	}
	input, err := translateMessages(request.Messages())
	if err != nil {
		return responses.ResponseNewParams{}, nil, err
	}
	definitions := request.Tools()
	translatedTools := make([]responses.ToolUnionParam, 0, len(definitions))
	allowedTools := make(map[string]struct{}, len(definitions))
	for index, definition := range definitions {
		translated, translateErr := translateTool(definition)
		if translateErr != nil {
			return responses.ResponseNewParams{}, nil, fmt.Errorf("translate tool %d: %w", index, translateErr)
		}
		translatedTools = append(translatedTools, translated)
		allowedTools[definition.Name()] = struct{}{}
	}
	params := responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input},
		Model: request.Model(),
		Store: param.NewOpt(false),
		Tools: translatedTools,
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return responses.ResponseNewParams{}, nil, fmt.Errorf("encode translated request: %w", err)
	}
	if len(encoded) > maxTranslatedRequestBytes {
		return responses.ResponseNewParams{}, nil, fmt.Errorf("translated request exceeds %d bytes", maxTranslatedRequestBytes)
	}
	return params, allowedTools, nil
}

func translateMessages(messages []message.Message) ([]responses.ResponseInputItemUnionParam, error) {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(messages))
	for messageIndex, value := range messages {
		translated, err := translateMessage(messageIndex, value)
		if err != nil {
			return nil, err
		}
		if len(translated) > maxTranslatedInputItems-len(items) {
			return nil, fmt.Errorf("translated input item count exceeds %d", maxTranslatedInputItems)
		}
		items = append(items, translated...)
	}
	return items, nil
}

type messageTranslator struct {
	index        int
	role         message.Role
	responseRole responses.EasyInputMessageRole
	text         strings.Builder
	items        []responses.ResponseInputItemUnionParam
}

func translateMessage(index int, value message.Message) ([]responses.ResponseInputItemUnionParam, error) {
	role, err := translateRole(value.Role())
	if err != nil {
		return nil, fmt.Errorf("message %d: %w", index, err)
	}
	translator := messageTranslator{index: index, role: value.Role(), responseRole: role}
	for partIndex, part := range value.Parts() {
		if err = translator.translatePart(partIndex, part); err != nil {
			return nil, err
		}
	}
	if err = translator.flushText(); err != nil {
		return nil, fmt.Errorf("message %d: %w", index, err)
	}
	return translator.items, nil
}

func (translator *messageTranslator) translatePart(index int, part message.Part) error {
	switch part.Kind() {
	case message.PartText:
		content, ok := part.TextValue()
		if !ok {
			return fmt.Errorf("message %d part %d has invalid text", translator.index, index)
		}
		translator.text.WriteString(content)
		return nil
	case message.PartToolCall:
		if translator.role != message.RoleAssistant {
			return fmt.Errorf("message %d part %d tool call requires assistant role", translator.index, index)
		}
		if err := translator.flushText(); err != nil {
			return fmt.Errorf("message %d: %w", translator.index, err)
		}
		translator.items = append(translator.items,
			responses.ResponseInputItemParamOfFunctionCall(string(part.Data()), part.CallID(), part.Name()))
		return nil
	case message.PartToolResult:
		if translator.role != message.RoleTool {
			return fmt.Errorf("message %d part %d tool result requires tool role", translator.index, index)
		}
		if err := translator.flushText(); err != nil {
			return fmt.Errorf("message %d: %w", translator.index, err)
		}
		translator.items = append(translator.items,
			responses.ResponseInputItemParamOfFunctionCallOutput(part.CallID(), string(part.Data())))
		return nil
	case message.PartExtension:
		return fmt.Errorf("message %d part %d extension namespace %q is unsupported", translator.index, index, part.Namespace())
	default:
		return fmt.Errorf("message %d part %d kind %q is unsupported", translator.index, index, part.Kind())
	}
}

func (translator *messageTranslator) flushText() error {
	if translator.text.Len() == 0 {
		return nil
	}
	if translator.role == message.RoleTool {
		return errors.New("tool message cannot contain text")
	}
	translator.items = append(translator.items,
		responses.ResponseInputItemParamOfMessage(translator.text.String(), translator.responseRole))
	translator.text.Reset()
	return nil
}

func translateRole(role message.Role) (responses.EasyInputMessageRole, error) {
	switch role {
	case message.RoleSystem:
		return responses.EasyInputMessageRoleSystem, nil
	case message.RoleUser:
		return responses.EasyInputMessageRoleUser, nil
	case message.RoleAssistant:
		return responses.EasyInputMessageRoleAssistant, nil
	case message.RoleTool:
		return responses.EasyInputMessageRole(""), nil
	default:
		return "", fmt.Errorf("role %q is unsupported", role)
	}
}

func translateTool(definition tool.Definition) (responses.ToolUnionParam, error) {
	if err := definition.Validate(); err != nil {
		return responses.ToolUnionParam{}, err
	}
	var schema map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(definition.InputSchema())))
	decoder.UseNumber()
	if err := decoder.Decode(&schema); err != nil || schema == nil {
		return responses.ToolUnionParam{}, errors.New("tool input schema must be a JSON object")
	}
	translated := responses.ToolParamOfFunction(definition.Name(), schema, false)
	translated.OfFunction.Description = param.NewOpt(definition.Description())
	return translated, nil
}

type stream struct {
	operationDone <-chan struct{}
	operationErr  func() error
	cancel        context.CancelFunc
	source        responseSource
	buffered      *responses.ResponseStreamEventUnion
	raw           chan rawResult
	pumpDone      chan struct{}
	pending       []model.StreamEvent
	allowedTools  map[string]struct{}
	seenCalls     map[string]struct{}
	text          strings.Builder
	terminal      bool
	closed        atomic.Bool
	closeOnce     sync.Once
	closeErr      error
}

var _ model.Stream = (*stream)(nil)

func (value *stream) Recv(ctx context.Context) (model.StreamEvent, error) {
	if value == nil || value.source == nil {
		return model.StreamEvent{}, errors.New("OpenAI stream is unavailable")
	}
	if ctx == nil {
		return model.StreamEvent{}, errors.New("OpenAI receive context is nil")
	}
	if value.closed.Load() || value.terminal && len(value.pending) == 0 {
		return model.StreamEvent{}, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return model.StreamEvent{}, err
	}
	if event, ok := value.popPending(); ok {
		return event, nil
	}
	return value.receive(ctx)
}

func (value *stream) receive(ctx context.Context) (model.StreamEvent, error) {
	for {
		raw, ok, nextErr := value.nextRaw(ctx)
		if nextErr != nil {
			if err := ctx.Err(); err != nil {
				return model.StreamEvent{}, err
			}
			return model.StreamEvent{}, providerStreamError(nextErr)
		}
		if !ok {
			return model.StreamEvent{}, io.EOF
		}
		events, terminal, err := value.translate(raw)
		if err != nil {
			return model.StreamEvent{}, protocolStreamError()
		}
		value.terminal = value.terminal || terminal
		value.pending = append(value.pending, events...)
		if event, available := value.popPending(); available {
			return event, nil
		}
	}
}

func (value *stream) Close() error {
	if value == nil || value.source == nil {
		return nil
	}
	value.closeOnce.Do(func() {
		value.closed.Store(true)
		if value.cancel != nil {
			value.cancel()
		}
		if err := value.source.Close(); err != nil {
			value.closeErr = providerStreamError(normalizeFailure(err, false))
		}
		if value.pumpDone != nil {
			<-value.pumpDone
		}
	})
	return value.closeErr
}

func providerStreamError(cause error) error {
	failure, ok := errors.AsType[*Error](cause)
	if !ok {
		failure = normalizeFailure(cause, false)
	}
	metadata, err := errorMetadata(failure)
	if err != nil {
		return err
	}
	problem, err := model.NewProblem(failure.code, "OpenAI response stream failed", false, metadata...)
	if err != nil {
		return fmt.Errorf("construct OpenAI stream problem: %w", err)
	}
	result, err := model.NewStreamError(problem, failure)
	if err != nil {
		return fmt.Errorf("construct OpenAI stream error: %w", err)
	}
	return result
}

func protocolStreamError() error {
	problem, err := model.NewProblem("protocol_error", "OpenAI response stream was invalid", false)
	if err != nil {
		return fmt.Errorf("construct OpenAI protocol problem: %w", err)
	}
	result, err := model.NewStreamError(problem, nil)
	if err != nil {
		return fmt.Errorf("construct OpenAI protocol error: %w", err)
	}
	return result
}

func (value *stream) popPending() (model.StreamEvent, bool) {
	if len(value.pending) == 0 {
		return model.StreamEvent{}, false
	}
	event := value.pending[0]
	value.pending = value.pending[1:]
	return event, true
}

type rawResult struct {
	event responses.ResponseStreamEventUnion
	err   error
}

func (value *stream) pump() {
	defer close(value.pumpDone)
	defer close(value.raw)
	for value.source.Next() {
		select {
		case value.raw <- rawResult{event: value.source.Current()}:
		case <-value.operationDone:
			return
		}
	}
	select {
	case value.raw <- rawResult{err: value.source.Err()}:
	case <-value.operationDone:
	}
}

func (value *stream) nextRaw(ctx context.Context) (responses.ResponseStreamEventUnion, bool, error) {
	if value.buffered != nil {
		current := *value.buffered
		value.buffered = nil
		return current, true, nil
	}
	select {
	case <-ctx.Done():
		return responses.ResponseStreamEventUnion{}, false, ctx.Err()
	case <-value.operationDone:
		return responses.ResponseStreamEventUnion{}, false, value.operationErr()
	case result, ok := <-value.raw:
		if !ok {
			return responses.ResponseStreamEventUnion{}, false, value.endError()
		}
		if result.err != nil {
			return responses.ResponseStreamEventUnion{}, false, normalizeFailure(result.err, false)
		}
		if result.event.Type == "" {
			return responses.ResponseStreamEventUnion{}, false, value.endError()
		}
		return result.event, true, nil
	}
}

func (value *stream) endError() error {
	if value.operationErr != nil {
		if err := value.operationErr(); err != nil {
			return err
		}
	}
	if value.terminal {
		return nil
	}
	return errors.New("OpenAI response stream ended before a terminal event")
}

func (value *stream) translate(raw responses.ResponseStreamEventUnion) ([]model.StreamEvent, bool, error) {
	switch event := raw.AsAny().(type) {
	case responses.ResponseTextDeltaEvent:
		return value.translateText(event.Delta)
	case responses.ResponseRefusalDeltaEvent:
		return value.translateText(event.Delta)
	case responses.ResponseCompletedEvent:
		events, err := value.translateCompleted(event.Response)
		return events, true, err
	case responses.ResponseFailedEvent:
		metadata, metadataErr := responseMetadata(event.Response)
		if metadataErr != nil {
			return nil, true, metadataErr
		}
		result, err := failedEvent(string(event.Response.Error.Code), "OpenAI response failed", metadata...)
		return []model.StreamEvent{result}, true, err
	case responses.ResponseIncompleteEvent:
		reason := safeCode(event.Response.IncompleteDetails.Reason)
		metadata, metadataErr := responseMetadata(event.Response)
		if metadataErr != nil {
			return nil, true, metadataErr
		}
		result, err := failedEvent("response_incomplete", "OpenAI response incomplete: "+reason, metadata...)
		return []model.StreamEvent{result}, true, err
	case responses.ResponseErrorEvent:
		result, err := failedEvent(safeCode(event.Code), "OpenAI response stream failed")
		return []model.StreamEvent{result}, true, err
	default:
		return nil, false, nil
	}
}

func (value *stream) translateText(text string) ([]model.StreamEvent, bool, error) {
	if text == "" {
		return nil, false, errors.New("OpenAI stream emitted an empty text delta")
	}
	if value.text.Len()+len(text) > model.MaximumOperationTextBytes {
		return nil, false, fmt.Errorf("OpenAI response text exceeds %d bytes", model.MaximumOperationTextBytes)
	}
	value.text.WriteString(text)
	events, err := textEvents(text)
	return events, false, err
}

func (value *stream) translateCompleted(response responses.Response) ([]model.StreamEvent, error) {
	if len(response.Output) > maxResponseOutputItems {
		return nil, fmt.Errorf("OpenAI response output item count exceeds %d", maxResponseOutputItems)
	}
	var completeText strings.Builder
	events := make([]model.StreamEvent, 0, len(response.Output)+1)
	callCount := 0
	for index, item := range response.Output {
		translated, err := value.translateOutput(index, callCount, item)
		if err != nil {
			return nil, err
		}
		completeText.WriteString(translated.text)
		if translated.hasEvent {
			callCount++
			events = append(events, translated.event)
		}
	}
	if completeText.Len() > model.MaximumOperationTextBytes {
		return nil, fmt.Errorf("OpenAI completed response text exceeds %d bytes", model.MaximumOperationTextBytes)
	}
	observedText := value.text.String()
	if completeText.String() != observedText {
		if !strings.HasPrefix(completeText.String(), observedText) {
			return nil, errors.New("OpenAI completed response text differs from streamed text")
		}
		missing := strings.TrimPrefix(completeText.String(), observedText)
		translated, _, err := value.translateText(missing)
		if err != nil {
			return nil, err
		}
		events = append(translated, events...)
	}
	if response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 {
		return nil, errors.New("OpenAI response usage contains negative tokens")
	}
	metadata, err := responseMetadata(response)
	if err != nil {
		return nil, err
	}
	completed, err := model.Completed(model.NewUsage(
		uint64(response.Usage.InputTokens),
		uint64(response.Usage.OutputTokens),
	), metadata...)
	if err != nil {
		return nil, err
	}
	events = append(events, completed)
	return events, nil
}

type translatedOutput struct {
	text     string
	event    model.StreamEvent
	hasEvent bool
}

func (value *stream) translateOutput(index, callCount int, item responses.ResponseOutputItemUnion) (translatedOutput, error) {
	switch output := item.AsAny().(type) {
	case responses.ResponseOutputMessage:
		text, err := outputMessageText(index, output)
		return translatedOutput{text: text}, err
	case responses.ResponseReasoningItem:
		// Reasoning is intentionally neither exposed nor retained by the core contract.
		return translatedOutput{}, nil
	case responses.ResponseFunctionToolCall:
		event, err := value.translateFunctionCall(callCount, output)
		return translatedOutput{event: event, hasEvent: err == nil}, err
	default:
		return translatedOutput{}, fmt.Errorf("OpenAI response output item %d type %q is unsupported", index, item.Type)
	}
}

func outputMessageText(index int, output responses.ResponseOutputMessage) (string, error) {
	var text strings.Builder
	for _, content := range output.Content {
		switch part := content.AsAny().(type) {
		case responses.ResponseOutputText:
			text.WriteString(part.Text)
		case responses.ResponseOutputRefusal:
			text.WriteString(part.Refusal)
		default:
			return "", fmt.Errorf("OpenAI response message item %d contains unsupported content", index)
		}
	}
	return text.String(), nil
}

func (value *stream) translateFunctionCall(index int, output responses.ResponseFunctionToolCall) (model.StreamEvent, error) {
	if index >= model.MaximumOperationToolCalls {
		return model.StreamEvent{}, fmt.Errorf("OpenAI response tool call count exceeds %d", model.MaximumOperationToolCalls)
	}
	if _, allowed := value.allowedTools[output.Name]; !allowed {
		return model.StreamEvent{}, fmt.Errorf("OpenAI response called undeclared tool %q", output.Name)
	}
	if _, duplicate := value.seenCalls[output.CallID]; duplicate {
		return model.StreamEvent{}, fmt.Errorf("OpenAI response duplicated tool call ID %q", output.CallID)
	}
	call, err := tool.NewCall(tool.CallID(output.CallID), output.Name, json.RawMessage(output.Arguments))
	if err != nil {
		return model.StreamEvent{}, fmt.Errorf("translate OpenAI tool call %d: %w", index, err)
	}
	translated, err := model.ToolCallEvent(call)
	if err != nil {
		return model.StreamEvent{}, err
	}
	value.seenCalls[output.CallID] = struct{}{}
	return translated, nil
}

func textEvents(text string) ([]model.StreamEvent, error) {
	if text == "" {
		return nil, nil
	}
	events := make([]model.StreamEvent, 0, len(text)/translatedTextChunkBytes+1)
	for len(text) > 0 {
		end := min(len(text), translatedTextChunkBytes)
		for end > 0 && end < len(text) && text[end]&0xc0 == 0x80 {
			end--
		}
		if end == 0 {
			end = min(len(text), translatedTextChunkBytes)
		}
		translated, err := model.TextDelta(text[:end])
		if err != nil {
			return nil, err
		}
		events = append(events, translated)
		text = text[end:]
	}
	return events, nil
}

func failedEvent(code, message string, metadata ...model.Metadata) (model.StreamEvent, error) {
	problem, err := model.NewProblem(safeCode(code), message, false, metadata...)
	if err != nil {
		return model.StreamEvent{}, err
	}
	return model.Failed(problem)
}

type metadataValue struct {
	ResponseID  string `json:"response_id,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	Model       string `json:"model,omitempty"`
	Status      string `json:"status,omitempty"`
	ServiceTier string `json:"service_tier,omitempty"`
	StatusCode  int    `json:"status_code,omitempty"`
}

func responseMetadata(response responses.Response) ([]model.Metadata, error) {
	value := metadataValue{
		ResponseID:  boundedMetadataFact(response.ID),
		Model:       boundedMetadataFact(response.Model),
		Status:      boundedMetadataFact(string(response.Status)),
		ServiceTier: boundedMetadataFact(string(response.ServiceTier)),
	}
	return newMetadata(value)
}

func errorMetadata(failure *Error) ([]model.Metadata, error) {
	if failure == nil {
		return nil, nil
	}
	return newMetadata(metadataValue{
		RequestID:  boundedMetadataFact(failure.requestID),
		StatusCode: failure.statusCode,
	})
}

func newMetadata(value metadataValue) ([]model.Metadata, error) {
	if value == (metadataValue{}) {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI metadata: %w", err)
	}
	metadata, err := model.NewMetadata(MetadataNamespace, encoded)
	if err != nil {
		return nil, fmt.Errorf("construct OpenAI metadata: %w", err)
	}
	return []model.Metadata{metadata}, nil
}

func boundedMetadataFact(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxMetadataFactBytes {
		return ""
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return ""
		}
	}
	return value
}
