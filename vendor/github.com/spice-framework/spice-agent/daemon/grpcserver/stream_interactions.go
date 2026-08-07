package grpcserver

import (
	"context"
	"errors"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	"github.com/spice-framework/spice-agent/daemon"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var errInteractionObservationFenced = errors.New("interaction observation was fenced")

type interactionObservation interface {
	Snapshot() daemon.PendingSnapshot
	Deltas() <-chan daemon.Delta
	Tailing() bool
	Context() context.Context
	Wait(context.Context) error
	Close()
}

// StreamInteractions authenticates one negotiated ownership epoch and sends a
// complete current pending-interaction snapshot before any optional live tail.
func (service *engineService) StreamInteractions(
	request *enginev1.StreamInteractionsRequest,
	stream grpc.ServerStreamingServer[enginev1.StreamInteractionsResponse],
) error {
	if stream == nil {
		return unauthenticatedTransport()
	}
	rpcContext := stream.Context()
	if err := service.requireAuthenticated(rpcContext); err != nil {
		return err
	}
	ctx, releaseContext := service.streamContext(rpcContext)
	defer releaseContext()
	current := &commonv1.ProtocolVersion{
		Major: commonv1.ProtocolMajor,
		Minor: commonv1.ProtocolMinor,
		Patch: commonv1.ProtocolPatch,
	}
	if !protocolValid(enginev1.ValidateStreamInteractionsRequest(request, current, service.limits)) {
		return sendInteractionFailure(
			stream, invalidLifecycleRequest("interaction stream request is invalid"), service.limits,
		)
	}
	negotiated, failure := service.lifecycleSession(request.GetClientId(), request.GetOwnershipEpoch())
	if failure != nil {
		return sendInteractionFailure(stream, failure, service.limits)
	}
	limits := negotiated.response.GetLimits()
	if !protocolValid(enginev1.ValidateInteractionStreamProtocol(negotiated.response.GetProtocol())) {
		return sendInteractionFailure(
			stream, interactionVersionMismatchStatus(negotiated.response.GetProtocol()), limits,
		)
	}
	if !protocolValid(enginev1.ValidateStreamInteractionsRequest(
		request, negotiated.response.GetProtocol(), limits,
	)) {
		return sendInteractionFailure(
			stream, invalidLifecycleRequest("interaction stream request exceeds the negotiated contract"), limits,
		)
	}

	observation, err := service.openInteractionObservation(ctx, negotiated.session, request.GetTail())
	if err != nil {
		if transportErr := contextTransportError(rpcContext, rpcContext.Err()); transportErr != nil {
			return transportErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return sendInteractionFailure(stream, interactionStreamFailureStatus(err), limits)
	}
	return serveOwnedInteractionObservation(stream, observation, request.GetTail(), limits)
}

func (service *engineService) openInteractionObservation(
	ctx context.Context,
	session daemon.Session,
	tail bool,
) (interactionObservation, error) {
	if tail {
		return service.host.SubscribeInteractions(ctx, session)
	}
	return service.host.SnapshotInteractions(ctx, session)
}

func serveOwnedInteractionObservation(
	stream grpc.ServerStreamingServer[enginev1.StreamInteractionsResponse],
	observation interactionObservation,
	expectedTail bool,
	limits *commonv1.Limits,
) error {
	if observation != nil {
		defer observation.Close()
	}
	return serveInteractionObservation(stream, observation, expectedTail, limits)
}

func serveInteractionObservation(
	stream grpc.ServerStreamingServer[enginev1.StreamInteractionsResponse],
	observation interactionObservation,
	expectedTail bool,
	limits *commonv1.Limits,
) error {
	if observation == nil || observation.Tailing() != expectedTail {
		return sendInteractionFailure(
			stream, internalLifecycleFailure("interaction observation is invalid"), limits,
		)
	}
	snapshot, err := interactionSnapshotToWire(observation.Snapshot(), limits)
	if err != nil {
		return sendInteractionFailure(
			stream, internalLifecycleFailure("interaction snapshot is invalid"), limits,
		)
	}
	control := &enginev1.InteractionStreamControl{
		Status: commonv1.OKStatus(), LatestRevision: snapshot.GetRevision(),
		PageLastRevision: snapshot.GetRevision(), Tailing: expectedTail,
	}
	initial := []*enginev1.StreamInteractionsResponse{
		{Payload: &enginev1.StreamInteractionsResponse_Snapshot{Snapshot: snapshot}},
		{Payload: &enginev1.StreamInteractionsResponse_Control{Control: control}},
	}
	if !protocolValid(enginev1.ValidateInteractionStreamPage(initial, expectedTail, limits)) {
		return sendInteractionFailure(
			stream, internalLifecycleFailure("interaction stream initial page is invalid"), limits,
		)
	}
	var validator *enginev1.InteractionTailValidator
	if expectedTail {
		validator, err = enginev1.NewInteractionTailValidator(snapshot, control, limits)
		if err != nil {
			return sendInteractionFailure(
				stream, internalLifecycleFailure("interaction stream validator is unavailable"), limits,
			)
		}
	}
	for index, frame := range initial {
		if err = sendInteractionFrame(stream, observation, frame); err != nil {
			if errors.Is(err, errInteractionObservationFenced) {
				if index == 0 {
					return interactionObservationEnded(stream, observation, limits)
				}
				return incompleteInteractionPage(stream.Context())
			}
			return err
		}
	}
	if !expectedTail {
		return nil
	}
	return tailInteractionObservation(stream, observation, validator, limits)
}

func tailInteractionObservation(
	stream grpc.ServerStreamingServer[enginev1.StreamInteractionsResponse],
	observation interactionObservation,
	validator *enginev1.InteractionTailValidator,
	limits *commonv1.Limits,
) error {
	for {
		select {
		case <-stream.Context().Done():
			return contextTransportError(stream.Context(), stream.Context().Err())
		case <-observation.Context().Done():
			return interactionObservationEnded(stream, observation, limits)
		case delta, open := <-observation.Deltas():
			if !open {
				return interactionObservationEnded(stream, observation, limits)
			}
			wire, err := interactionDeltaToWire(delta)
			if err != nil {
				return sendInteractionFailure(
					stream, internalLifecycleFailure("interaction delta is invalid"), limits,
				)
			}
			frame := &enginev1.StreamInteractionsResponse{
				Payload: &enginev1.StreamInteractionsResponse_Delta{Delta: wire},
			}
			if !protocolValid(validator.Accept(frame)) {
				return sendInteractionFailure(
					stream, internalLifecycleFailure("interaction delta violates the stream contract"), limits,
				)
			}
			if err = sendInteractionFrame(stream, observation, frame); err != nil {
				if errors.Is(err, errInteractionObservationFenced) {
					return interactionObservationEnded(stream, observation, limits)
				}
				return err
			}
		}
	}
}

func interactionObservationEnded(
	stream grpc.ServerStreamingServer[enginev1.StreamInteractionsResponse],
	observation interactionObservation,
	limits *commonv1.Limits,
) error {
	if transportErr := contextTransportError(stream.Context(), stream.Context().Err()); transportErr != nil {
		return transportErr
	}
	if cause := context.Cause(observation.Context()); cause != nil {
		// Reconnect and daemon shutdown fence the old epoch before it may emit
		// another frame. Closing the observation releases that fence.
		//nolint:nilerr // a fenced old-epoch stream intentionally ends with quiet EOF.
		return nil
	}
	err := observation.Wait(context.Background())
	if err == nil {
		return nil
	}
	return sendInteractionFailure(stream, interactionStreamFailureStatus(err), limits)
}

func sendInteractionFrame(
	stream grpc.ServerStreamingServer[enginev1.StreamInteractionsResponse],
	observation interactionObservation,
	frame *enginev1.StreamInteractionsResponse,
) error {
	select {
	case <-stream.Context().Done():
		return contextTransportError(stream.Context(), stream.Context().Err())
	case <-observation.Context().Done():
		return errInteractionObservationFenced
	default:
		return interactionSendError(stream.Context(), stream.Send(frame))
	}
}

func incompleteInteractionPage(ctx context.Context) error {
	if transportErr := contextTransportError(ctx, ctx.Err()); transportErr != nil {
		return transportErr
	}
	return status.Error(codes.Unavailable, "interaction stream initial page was interrupted")
}

func sendInteractionFailure(
	stream grpc.ServerStreamingServer[enginev1.StreamInteractionsResponse],
	statusValue *commonv1.Status,
	limits *commonv1.Limits,
) error {
	control := &enginev1.InteractionStreamControl{Status: statusValue}
	frame := &enginev1.StreamInteractionsResponse{
		Payload: &enginev1.StreamInteractionsResponse_Control{Control: control},
	}
	if enginev1.ValidateInteractionStreamControl(control) != nil ||
		commonv1.ValidateEncodedSize(frame, limits.GetMaxMessageBytes()) != nil {
		control = &enginev1.InteractionStreamControl{Status: internalLifecycleStatus()}
		frame = &enginev1.StreamInteractionsResponse{
			Payload: &enginev1.StreamInteractionsResponse_Control{Control: control},
		}
		if enginev1.ValidateInteractionStreamControl(control) != nil ||
			commonv1.ValidateEncodedSize(frame, limits.GetMaxMessageBytes()) != nil {
			return status.Error(
				codes.ResourceExhausted,
				"interaction stream control exceeds negotiated message limit",
			)
		}
	}
	return interactionSendError(stream.Context(), stream.Send(frame))
}

func interactionStreamFailureStatus(err error) *commonv1.Status {
	if exhausted, ok := errors.AsType[*daemon.ObserverExhaustedError](err); ok {
		return overloadLifecycleStatus(
			"interaction observer capacity is exhausted",
			exhausted.Resource(), exhausted.Limit(), exhausted.Observed(),
		)
	}
	return lifecycleStatusWithoutOperation(err)
}

func interactionVersionMismatchStatus(observed *commonv1.ProtocolVersion) *commonv1.Status {
	clientVersion := proto.CloneOf(observed)
	requiredVersion := &commonv1.ProtocolVersion{
		Major: commonv1.ProtocolMajor, Minor: enginev1.InteractionStreamMinimumMinor,
		Patch: commonv1.ProtocolPatch,
	}
	return &commonv1.Status{
		Code:    commonv1.ErrorCode_ERROR_CODE_INCOMPATIBLE_VERSION,
		Message: "interaction streams require protocol minor 2",
		Detail: &commonv1.Status_VersionMismatch{VersionMismatch: &commonv1.VersionMismatch{
			Client: &commonv1.ProtocolRange{
				Minimum: clientVersion, Maximum: proto.CloneOf(clientVersion),
			},
			Server: &commonv1.ProtocolRange{
				Minimum: requiredVersion, Maximum: proto.CloneOf(requiredVersion),
			},
		}},
	}
}

func interactionSendError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if transportErr := contextTransportError(ctx, err); transportErr != nil {
		return transportErr
	}
	return status.Error(codes.Unavailable, "interaction stream transport failed")
}

func interactionSnapshotToWire(
	value daemon.PendingSnapshot,
	limits *commonv1.Limits,
) (*enginev1.InteractionSnapshot, error) {
	pending := make([]*enginev1.PendingInteraction, len(value.Pending))
	for index, item := range value.Pending {
		wire, err := pendingInteractionToWire(item)
		if err != nil {
			return nil, err
		}
		pending[index] = wire
	}
	result := &enginev1.InteractionSnapshot{Revision: value.Revision, Pending: pending}
	if err := enginev1.ValidateInteractionSnapshot(result, limits); err != nil {
		return nil, err
	}
	return result, nil
}

func interactionDeltaToWire(value daemon.Delta) (*enginev1.InteractionDelta, error) {
	interactionValue, err := pendingInteractionToWire(value.Pending)
	if err != nil {
		return nil, err
	}
	var kind enginev1.InteractionDeltaKind
	switch value.Kind {
	case daemon.DeltaOpened:
		kind = enginev1.InteractionDeltaKind_INTERACTION_DELTA_KIND_OPENED
	case daemon.DeltaClosed:
		kind = enginev1.InteractionDeltaKind_INTERACTION_DELTA_KIND_CLOSED
	default:
		return nil, errors.New("interaction delta kind is unsupported")
	}
	result := &enginev1.InteractionDelta{
		Revision: value.Revision, Kind: kind, Interaction: interactionValue,
	}
	if err = enginev1.ValidateInteractionDelta(result); err != nil {
		return nil, err
	}
	return result, nil
}

func pendingInteractionToWire(value daemon.Pending) (*enginev1.PendingInteraction, error) {
	if err := value.Scope.Validate(); err != nil {
		return nil, err
	}
	if err := value.Request.Validate(); err != nil {
		return nil, err
	}
	result := &enginev1.PendingInteraction{
		RunId: value.Scope.RunID(), InteractionId: string(value.Request.ID()),
		Kind: value.Request.Kind(), Prompt: value.Request.Prompt(), SchemaJson: value.Request.Schema(),
	}
	if err := enginev1.ValidatePendingInteraction(result); err != nil {
		return nil, err
	}
	return result, nil
}
