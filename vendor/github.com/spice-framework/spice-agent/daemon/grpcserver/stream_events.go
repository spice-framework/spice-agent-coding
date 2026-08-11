package grpcserver

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/client"
	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	"github.com/spice-framework/spice-agent/daemon"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
	"github.com/spice-framework/spice-agent/event"
	"github.com/spice-framework/spice-agent/tool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StreamEvents delivers one prevalidated retained page and, when requested at
// its captured end, the atomically registered live tail. Application failures
// are terminal control frames; only authentication, cancellation, deadlines,
// and failed transport sends escape as gRPC errors.
func (service *engineService) StreamEvents(
	request *enginev1.StreamEventsRequest,
	stream grpc.ServerStreamingServer[enginev1.StreamEventsResponse],
) error {
	rpcContext := stream.Context()
	if err := service.requireAuthenticated(rpcContext); err != nil {
		return err
	}
	ctx, releaseContext := service.streamContext(rpcContext)
	defer releaseContext()
	if !protocolValid(enginev1.ValidateStreamEventsRequest(request, service.limits)) {
		return sendEventFailure(stream, service.limits, &enginev1.StreamControl{
			Status: invalidLifecycleRequest("event stream request is invalid"),
		})
	}
	negotiated, failure := service.lifecycleSession(request.GetClientId(), request.GetOwnershipEpoch())
	if failure != nil {
		return sendEventFailure(stream, service.limits, &enginev1.StreamControl{Status: failure})
	}
	limits := negotiated.response.GetLimits()
	if !protocolValid(enginev1.ValidateStreamEventsRequest(request, limits)) {
		return sendEventFailure(stream, limits, &enginev1.StreamControl{
			Status: invalidLifecycleRequest("event stream request exceeds negotiated limits"),
		})
	}
	maxEvents, eventsFit := platformInt(uint64(request.GetReplayLimit()))
	maxBytes, bytesFit := platformInt(limits.GetMaxReplayBytes())
	if !eventsFit || !bytesFit {
		return sendEventFailure(stream, limits, &enginev1.StreamControl{
			Status: internalLifecycleFailure("negotiated event replay limits exceed platform capacity"),
		})
	}
	run, err := client.NewRunRef(request.GetRunId())
	if err != nil {
		return sendEventFailure(stream, limits, &enginev1.StreamControl{
			Status: invalidLifecycleRequest("event stream request is invalid"),
		})
	}
	observation, err := service.host.ReplayEvents(ctx, negotiated.session, run, event.ReplayRequest{
		AfterSequence: request.GetAfterSequence(),
		MaxEvents:     maxEvents,
		MaxBytes:      maxBytes,
		Tail:          request.GetTail(),
	})
	if err != nil {
		return service.sendReplayFailure(stream, limits, request.GetAfterSequence(), err)
	}
	if observation == nil {
		return sendEventFailure(stream, limits, &enginev1.StreamControl{
			Status: internalLifecycleFailure("event replay result is unavailable"),
		})
	}
	return serveOwnedEventObservation(request, observation, limits, stream)
}

func platformInt(value uint64) (int, bool) {
	if value > uint64(math.MaxInt) {
		return 0, false
	}
	return int(value), true // #nosec G115 -- the platform maximum is checked immediately above.
}

func serveOwnedEventObservation(
	request *enginev1.StreamEventsRequest,
	observation ownedEventObservation,
	limits *commonv1.Limits,
	stream grpc.ServerStreamingServer[enginev1.StreamEventsResponse],
) error {
	defer observation.Close()
	return streamEventObservation(request, observation, limits, stream)
}

func streamEventObservation(
	request *enginev1.StreamEventsRequest,
	observation eventObservation,
	limits *commonv1.Limits,
	stream grpc.ServerStreamingServer[enginev1.StreamEventsResponse],
) error {
	page := observation.Page()
	events, control, err := prepareEventPage(request, page, limits)
	if err != nil {
		return sendEventFailure(stream, limits, &enginev1.StreamControl{
			Status: internalLifecycleFailure("event replay result is invalid"),
		})
	}
	for _, value := range events {
		if err = sendEventResponse(stream, limits, eventResponse(value)); err != nil {
			return eventSendError(stream.Context(), err)
		}
	}
	if err = sendEventResponse(stream, limits, controlResponse(control)); err != nil {
		return eventSendError(stream.Context(), err)
	}
	if !page.Tailing {
		return nil
	}
	return streamEventTail(request.GetRunId(), observation, page, limits, stream)
}

type eventObservation interface {
	Page() event.ReplayPage
	Context() context.Context
}

type ownedEventObservation interface {
	eventObservation
	Close()
}

func prepareEventPage(
	request *enginev1.StreamEventsRequest,
	page event.ReplayPage,
	limits *commonv1.Limits,
) ([]*enginev1.RunEvent, *enginev1.StreamControl, error) {
	if request == nil || page.Tailing != (page.Tail != nil) || page.HasMore && page.Tailing ||
		page.Tailing && !request.GetTail() || uint64(len(page.Events)) > uint64(request.GetReplayLimit()) {
		return nil, nil, errors.New("event replay page shape is invalid")
	}
	events := make([]*enginev1.RunEvent, len(page.Events))
	for index := range page.Events {
		value, err := eventToWire(page.Events[index])
		if err != nil {
			return nil, nil, err
		}
		events[index] = value
	}
	if err := enginev1.ValidateEventBatch(request.GetRunId(), request.GetAfterSequence(), events, limits); err != nil {
		return nil, nil, err
	}
	expectedPageLast := request.GetAfterSequence()
	if len(events) != 0 {
		expectedPageLast = events[len(events)-1].GetSequence()
	}
	if page.PageLastSequence != expectedPageLast {
		return nil, nil, errors.New("event replay page cursor does not match its events")
	}
	pageLast := page.PageLastSequence
	control := &enginev1.StreamControl{
		Status:                commonv1.OKStatus(),
		EarliestSequence:      page.EarliestSequence,
		LatestSequence:        page.LatestSequence,
		LastDeliveredSequence: page.PageLastSequence,
		Tailing:               page.Tailing,
		PageLastSequence:      &pageLast,
		HasMore:               page.HasMore,
	}
	if err := enginev1.ValidateStreamControl(control); err != nil {
		return nil, nil, err
	}
	for _, value := range events {
		if err := validateEventResponse(eventResponse(value), limits); err != nil {
			return nil, nil, err
		}
	}
	if err := validateEventResponse(controlResponse(control), limits); err != nil {
		return nil, nil, err
	}
	return events, control, nil
}

func streamEventTail(
	runID string,
	observation eventObservation,
	page event.ReplayPage,
	limits *commonv1.Limits,
	stream grpc.ServerStreamingServer[enginev1.StreamEventsResponse],
) error {
	next := page.PageLastSequence
	for {
		select {
		case <-observation.Context().Done():
			return eventFenceResult(stream.Context())
		case envelope, open := <-page.Tail.Events():
			if !open {
				return finishEventTail(stream, limits, page, page.Tail.Wait(context.Background()))
			}
			select {
			case <-observation.Context().Done():
				return eventFenceResult(stream.Context())
			default:
			}
			if next == math.MaxUint64 || envelope.Sequence() != next+1 {
				return sendEventFailure(stream, limits, eventTailFailure(
					internalLifecycleFailure("event tail is not contiguous"), page, next,
				))
			}
			value, err := eventToWire(envelope)
			if err != nil || value.GetRunId() != runID {
				return sendEventFailure(stream, limits, eventTailFailure(
					internalLifecycleFailure("event tail result is invalid"), page, next,
				))
			}
			response := eventResponse(value)
			if err = validateEventResponse(response, limits); err != nil {
				return sendEventFailure(stream, limits, eventTailFailure(
					internalLifecycleFailure("event tail result is invalid"), page, next,
				))
			}
			if err = stream.Send(response); err != nil {
				return eventSendError(stream.Context(), err)
			}
			next = value.GetSequence()
		}
	}
}

func eventFenceResult(streamContext context.Context) error {
	return contextTransportError(streamContext, streamContext.Err())
}

func finishEventTail(
	stream grpc.ServerStreamingServer[enginev1.StreamEventsResponse],
	limits *commonv1.Limits,
	page event.ReplayPage,
	err error,
) error {
	if err == nil {
		return nil
	}
	if transportErr := contextTransportError(stream.Context(), stream.Context().Err()); transportErr != nil {
		return transportErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Reconnect and daemon shutdown cancel the observation independently of
		// the old RPC. Closing quietly fences the old owner without disclosure.
		return nil
	}
	lastDelivered := page.PageLastSequence
	statusValue := internalLifecycleFailure("event tail failed")
	if exhausted, ok := errors.AsType[*event.ResourceExhaustedError](err); ok {
		lastDelivered = exhausted.LastDelivered
		statusValue = replayCapacityStatus(exhausted)
	}
	return sendEventFailure(stream, limits, eventTailFailure(statusValue, page, lastDelivered))
}

func eventTailFailure(statusValue *commonv1.Status, page event.ReplayPage, lastDelivered uint64) *enginev1.StreamControl {
	return &enginev1.StreamControl{
		Status: statusValue, EarliestSequence: page.EarliestSequence,
		LatestSequence: page.LatestSequence, LastDeliveredSequence: lastDelivered,
	}
}

func (service *engineService) sendReplayFailure(
	stream grpc.ServerStreamingServer[enginev1.StreamEventsResponse],
	limits *commonv1.Limits,
	after uint64,
	err error,
) error {
	if transportErr := contextTransportError(stream.Context(), stream.Context().Err()); transportErr != nil {
		return transportErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	control := &enginev1.StreamControl{}
	if outside, ok := errors.AsType[*event.OutOfRangeError](err); ok {
		statusValue := enginev1.CheckReplayCursor(after, outside.Earliest, outside.Latest)
		if statusValue != nil && outside.RequestedAfter == after &&
			statusValue.GetReplayBounds().GetRecoverySequence() == outside.RecoveryAfter {
			control.Status = statusValue
			control.EarliestSequence = outside.Earliest
			control.LatestSequence = outside.Latest
			control.LastDeliveredSequence = outside.RecoveryAfter
		} else {
			control.Status = internalLifecycleFailure("event replay recovery is invalid")
		}
	} else if exhausted, ok := errors.AsType[*event.ResourceExhaustedError](err); ok {
		control.Status = replayCapacityStatus(exhausted)
		control.LastDeliveredSequence = exhausted.LastDelivered
	} else {
		control.Status = lifecycleFailureStatus(err, "", "")
	}
	return sendEventFailure(stream, limits, control)
}

func replayCapacityStatus(failure *event.ResourceExhaustedError) *commonv1.Status {
	if failure == nil || failure.Resource() == "" || failure.Limit() == 0 || failure.Observed() <= failure.Limit() {
		return internalLifecycleFailure("event delivery failed")
	}
	return enginev1.OverloadStatus(failure.Resource(), failure.Limit(), failure.Observed())
}

func eventToWire(envelope event.Envelope) (*enginev1.RunEvent, error) {
	kind, ok := eventKindToWire(envelope.Kind())
	if !ok {
		return nil, errors.New("event kind is unsupported")
	}
	payload, err := eventPayloadToWire(envelope)
	if err != nil {
		return nil, err
	}
	value := &enginev1.RunEvent{
		RunId: envelope.RunID(), Sequence: envelope.Sequence(), UnixNano: envelope.At().UnixNano(),
		Kind: kind, PayloadJson: payload, Terminal: envelope.Terminal(),
	}
	if err := enginev1.ValidateRunEvent(value); err != nil {
		return nil, err
	}
	return value, nil
}

func eventPayloadToWire(envelope event.Envelope) ([]byte, error) {
	switch envelope.Kind() {
	case event.ToolStarted:
		return toolStartedPayloadToWire(envelope.Data())
	case event.ToolCompleted, event.ToolFailed:
		return toolTerminalPayloadToWire(envelope.Kind(), envelope.Data())
	default:
		return envelope.Data(), nil
	}
}

func toolStartedPayloadToWire(payload json.RawMessage) ([]byte, error) {
	occurrence, err := agent.DecodeToolStartedOccurrence(payload)
	if err != nil {
		return nil, errors.New("tool started event payload is invalid")
	}
	legacy, err := json.Marshal(struct {
		CallID string `json:"call_id"`
		Name   string `json:"name"`
	}{CallID: string(occurrence.CallID()), Name: occurrence.Name()})
	if err != nil {
		return nil, errors.New("encode legacy tool started event payload")
	}
	return legacy, nil
}

func toolTerminalPayloadToWire(kind event.Kind, payload json.RawMessage) ([]byte, error) {
	occurrence, err := agent.DecodeToolTerminalOccurrence(kind, payload)
	if err != nil {
		return nil, errors.New("tool terminal event payload is invalid")
	}
	problem := ""
	if kind == event.ToolFailed {
		problem = "tool execution failed"
	}
	legacy, err := json.Marshal(struct {
		CallID  string                `json:"call_id"`
		Name    string                `json:"name"`
		Error   string                `json:"error"`
		Outcome tool.ExecutionState   `json:"outcome,omitempty"`
		Retry   tool.RetryDisposition `json:"retry,omitempty"`
	}{
		CallID: string(occurrence.CallID()), Name: occurrence.Name(), Error: problem,
		Outcome: occurrence.ExecutionState(), Retry: occurrence.RetryDisposition(),
	})
	if err != nil {
		return nil, errors.New("encode legacy tool terminal event payload")
	}
	return legacy, nil
}

func eventKindToWire(kind event.Kind) (enginev1.EventKind, bool) {
	mappings := [...]struct {
		domain event.Kind
		wire   enginev1.EventKind
	}{
		{event.RunStarted, enginev1.EventKind_EVENT_KIND_RUN_STARTED},
		{event.RunCompleted, enginev1.EventKind_EVENT_KIND_RUN_COMPLETED},
		{event.RunFailed, enginev1.EventKind_EVENT_KIND_RUN_FAILED},
		{event.RunCancelled, enginev1.EventKind_EVENT_KIND_RUN_CANCELLED},
		{event.TurnStarted, enginev1.EventKind_EVENT_KIND_TURN_STARTED},
		{event.TurnCompleted, enginev1.EventKind_EVENT_KIND_TURN_COMPLETED},
		{event.TurnFailed, enginev1.EventKind_EVENT_KIND_TURN_FAILED},
		{event.ModelStarted, enginev1.EventKind_EVENT_KIND_MODEL_STARTED},
		{event.ModelDelta, enginev1.EventKind_EVENT_KIND_MODEL_DELTA},
		{event.ModelCompleted, enginev1.EventKind_EVENT_KIND_MODEL_COMPLETED},
		{event.ModelFailed, enginev1.EventKind_EVENT_KIND_MODEL_FAILED},
		{event.ToolStarted, enginev1.EventKind_EVENT_KIND_TOOL_STARTED},
		{event.ToolProgress, enginev1.EventKind_EVENT_KIND_TOOL_PROGRESS},
		{event.ToolCompleted, enginev1.EventKind_EVENT_KIND_TOOL_COMPLETED},
		{event.ToolFailed, enginev1.EventKind_EVENT_KIND_TOOL_FAILED},
		{event.InteractionStarted, enginev1.EventKind_EVENT_KIND_INTERACTION_STARTED},
		{event.InteractionCompleted, enginev1.EventKind_EVENT_KIND_INTERACTION_COMPLETED},
		{event.InteractionFailed, enginev1.EventKind_EVENT_KIND_INTERACTION_FAILED},
		{event.InteractionCancelled, enginev1.EventKind_EVENT_KIND_INTERACTION_CANCELLED},
	}
	for _, current := range mappings {
		if current.domain == kind {
			return current.wire, true
		}
	}
	return enginev1.EventKind_EVENT_KIND_UNSPECIFIED, false
}

func eventResponse(value *enginev1.RunEvent) *enginev1.StreamEventsResponse {
	return &enginev1.StreamEventsResponse{Payload: &enginev1.StreamEventsResponse_Event{Event: value}}
}

func controlResponse(value *enginev1.StreamControl) *enginev1.StreamEventsResponse {
	return &enginev1.StreamEventsResponse{Payload: &enginev1.StreamEventsResponse_Control{Control: value}}
}

func validateEventResponse(value *enginev1.StreamEventsResponse, limits *commonv1.Limits) error {
	if value == nil || (value.GetEvent() == nil) == (value.GetControl() == nil) {
		return errors.New("event stream response requires exactly one payload")
	}
	if value.GetEvent() != nil {
		if err := enginev1.ValidateRunEvent(value.GetEvent()); err != nil {
			return err
		}
	} else if err := enginev1.ValidateStreamControl(value.GetControl()); err != nil {
		return err
	}
	return commonv1.ValidateEncodedSize(value, limits.GetMaxMessageBytes())
}

func sendEventResponse(
	stream grpc.ServerStreamingServer[enginev1.StreamEventsResponse],
	limits *commonv1.Limits,
	response *enginev1.StreamEventsResponse,
) error {
	if err := validateEventResponse(response, limits); err != nil {
		return err
	}
	return stream.Send(response)
}

func sendEventFailure(
	stream grpc.ServerStreamingServer[enginev1.StreamEventsResponse],
	limits *commonv1.Limits,
	control *enginev1.StreamControl,
) error {
	if control == nil || control.GetStatus().GetCode() == commonv1.ErrorCode_ERROR_CODE_OK ||
		commonv1.ValidateStatus(control.GetStatus()) != nil {
		control = &enginev1.StreamControl{Status: internalLifecycleFailure("event stream failed")}
	}
	if err := sendEventResponse(stream, limits, controlResponse(control)); err != nil {
		return eventSendError(stream.Context(), err)
	}
	return nil
}

func eventSendError(ctx context.Context, err error) error {
	if transportErr := contextTransportError(ctx, err); transportErr != nil {
		return transportErr
	}
	return status.Error(codes.Unavailable, "event stream transport failed")
}

var _ ownedEventObservation = (*daemon.EventObservation)(nil)
