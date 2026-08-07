package grpcclient

import (
	"context"
	"errors"

	"github.com/spice-framework/spice-agent/client"
	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validatedStatusError(err error) *commonv1.StatusError {
	value, _ := errors.AsType[*commonv1.StatusError](err)
	return value
}

type statusContext struct {
	run          *client.RunRef
	operation    *client.OperationID
	after        *uint64
	sessionEpoch uint64
	readOnly     bool
}

func statusToError(value *commonv1.Status, expected statusContext) error {
	if commonv1.ValidateStatus(value) != nil || value.GetCode() == commonv1.ErrorCode_ERROR_CODE_OK {
		return protocolError()
	}
	facts, err := statusFacts(value, expected)
	if err != nil {
		return err
	}
	if code, ok := genericErrorCode(value.GetCode()); ok {
		return constructed(client.NewStatusError(code, facts))
	}
	return recoveryStatusError(value, facts, expected)
}

func statusFacts(value *commonv1.Status, expected statusContext) (client.ErrorFacts, error) {
	var operation *client.OperationID
	if value.GetOperationId() != "" {
		parsed, err := client.NewOperationID(value.GetOperationId())
		if err != nil {
			return client.ErrorFacts{}, protocolError()
		}
		operation = &parsed
	}
	if expected.readOnly && operation != nil || expected.operation != nil && operation != nil && *operation != *expected.operation {
		return client.ErrorFacts{}, protocolError()
	}
	facts, err := client.NewErrorFacts(value.GetMessage(), value.GetRetryable(), operation)
	if err != nil {
		return client.ErrorFacts{}, protocolError()
	}
	return facts, nil
}

func recoveryStatusError(
	value *commonv1.Status,
	facts client.ErrorFacts,
	expected statusContext,
) error {
	switch value.GetCode() {
	case commonv1.ErrorCode_ERROR_CODE_INCOMPATIBLE_VERSION:
		detail := value.GetVersionMismatch()
		clientRange, clientErr := protocolRangeFromWire(detail.GetClient())
		serverRange, serverErr := protocolRangeFromWire(detail.GetServer())
		if clientErr != nil || serverErr != nil {
			return protocolError()
		}
		return constructed(client.NewVersionMismatchError(facts, clientRange, serverRange))
	case commonv1.ErrorCode_ERROR_CODE_MISSING_CAPABILITY:
		detail := value.GetCapabilityMismatch()
		return constructed(client.NewCapabilityMismatchError(
			facts, detail.GetRequired(), detail.GetAvailable(), detail.GetMissing(),
		))
	case commonv1.ErrorCode_ERROR_CODE_OUT_OF_RANGE:
		if expected.run == nil {
			return protocolError()
		}
		detail := value.GetReplayBounds()
		if expected.after != nil && detail.GetRequestedAfterSequence() != *expected.after {
			return protocolError()
		}
		return constructed(client.NewCursorGapError(
			facts, *expected.run, detail.GetRequestedAfterSequence(), detail.GetEarliestSequence(),
			detail.GetLatestSequence(), detail.GetRecoverySequence(),
		))
	case commonv1.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED:
		detail := value.GetOverload()
		return constructed(client.NewOverloadError(
			facts, detail.GetResource(), detail.GetLimit(), detail.GetObserved(),
		))
	case commonv1.ErrorCode_ERROR_CODE_STALE_CLIENT:
		detail := value.GetStaleClient()
		if expected.sessionEpoch != 0 && detail.GetObservedEpoch() != expected.sessionEpoch {
			return protocolError()
		}
		return constructed(client.NewStaleSessionError(
			facts, detail.GetExpectedEpoch(), detail.GetObservedEpoch(),
		))
	case commonv1.ErrorCode_ERROR_CODE_UNCERTAIN_OPERATION:
		detail := value.GetUncertainOperation()
		uncertain, parseErr := client.NewOperationID(detail.GetOperationId())
		if parseErr != nil {
			return protocolError()
		}
		if expected.operation != nil && uncertain != *expected.operation {
			return protocolError()
		}
		return constructed(client.NewUncertainOperationError(facts, uncertain, detail.GetOperationKind()))
	case commonv1.ErrorCode_ERROR_CODE_SNAPSHOT_VERSION_MISMATCH:
		detail := value.GetSnapshotVersionMismatch()
		return constructed(client.NewSnapshotVersionMismatchError(
			facts, detail.GetExpected(), detail.GetObserved(),
		))
	default:
		return protocolError()
	}
}

func genericErrorCode(code commonv1.ErrorCode) (client.ErrorCode, bool) {
	switch code {
	case commonv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT:
		return client.ErrorInvalidArgument, true
	case commonv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED:
		return client.ErrorUnauthenticated, true
	case commonv1.ErrorCode_ERROR_CODE_NOT_FOUND:
		return client.ErrorNotFound, true
	case commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE:
		return client.ErrorUnavailable, true
	case commonv1.ErrorCode_ERROR_CODE_CANCELLED:
		return client.ErrorCancelled, true
	case commonv1.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED:
		return client.ErrorDeadlineExceeded, true
	case commonv1.ErrorCode_ERROR_CODE_CONFLICT:
		return client.ErrorConflict, true
	case commonv1.ErrorCode_ERROR_CODE_INTERNAL:
		return client.ErrorInternal, true
	default:
		return "", false
	}
}

func transportError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	code := status.Code(err)
	switch code {
	case codes.Canceled:
		return statusError(client.ErrorCancelled, "gRPC operation was cancelled", false)
	case codes.DeadlineExceeded:
		return statusError(client.ErrorDeadlineExceeded, "gRPC operation exceeded its deadline", true)
	case codes.Unauthenticated, codes.PermissionDenied:
		return statusError(client.ErrorUnauthenticated, "gRPC endpoint authentication failed", false)
	case codes.InvalidArgument, codes.OutOfRange:
		return invalidArgumentError("gRPC request was rejected")
	case codes.FailedPrecondition:
		return statusError(client.ErrorConflict, "gRPC request conflicts with current state", false)
	case codes.NotFound:
		return statusError(client.ErrorNotFound, "gRPC resource was not found", false)
	case codes.Aborted, codes.AlreadyExists:
		return statusError(client.ErrorConflict, "gRPC operation conflicted with current state", false)
	case codes.Unavailable:
		return unavailableError("gRPC endpoint is unavailable", true)
	case codes.ResourceExhausted:
		return statusError(client.ErrorInternal, "gRPC transport exceeded an untyped resource limit", false)
	default:
		return statusError(client.ErrorInternal, "gRPC transport failed", false)
	}
}

func mutationTransportError(ctx context.Context, err error, operation client.OperationID, kind string) error {
	if status.Code(err) == codes.Unauthenticated || status.Code(err) == codes.PermissionDenied {
		return transportError(ctx, err)
	}
	if ctx != nil && ctx.Err() == nil {
		switch status.Code(err) {
		case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.AlreadyExists:
			return transportError(ctx, err)
		}
	}
	facts, factsErr := client.NewErrorFacts("gRPC mutation outcome is uncertain", false, &operation)
	if factsErr != nil {
		return protocolError()
	}
	result := constructed(client.NewUncertainOperationError(facts, operation, kind))
	if ctx != nil && ctx.Err() != nil {
		return errors.Join(result, context.Cause(ctx))
	}
	return result
}

func reconnectTransportError(ctx context.Context, err error) error {
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied, codes.InvalidArgument, codes.OutOfRange:
		return transportError(ctx, err)
	default:
	}
	result := unavailableError(
		"reconnect ownership outcome is uncertain; do not retry the same claim",
		false,
	)
	if ctx != nil && ctx.Err() != nil {
		return errors.Join(result, context.Cause(ctx))
	}
	return result
}

func initializeTransportError(ctx context.Context, err error) error {
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied, codes.InvalidArgument, codes.OutOfRange:
		return transportError(ctx, err)
	default:
	}
	result := unavailableError(
		"fresh client initialization outcome is uncertain; do not retry automatically",
		false,
	)
	if ctx != nil && ctx.Err() != nil {
		return errors.Join(result, context.Cause(ctx))
	}
	return result
}

func initializationAttemptTransportError(
	ctx context.Context,
	err error,
	attempt client.InitializationAttemptID,
) error {
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied, codes.InvalidArgument, codes.OutOfRange:
		return transportError(ctx, err)
	}
	facts, factsErr := client.NewErrorFacts(
		"initialization outcome is uncertain; replay only the same immutable request and attempt ID",
		true,
		nil,
	)
	if factsErr != nil {
		return protocolError()
	}
	replay := constructed(client.NewInitializationReplayError(facts, attempt))
	if ctx != nil && context.Cause(ctx) != nil {
		return errors.Join(replay, context.Cause(ctx))
	}
	switch status.Code(err) {
	case codes.Canceled:
		return errors.Join(replay, context.Canceled)
	case codes.DeadlineExceeded:
		return errors.Join(replay, context.DeadlineExceeded)
	default:
		return replay
	}
}

func protocolError() error {
	return statusError(client.ErrorInternal, "daemon response violated the protocol contract", false)
}

func invalidArgumentError(message string) error {
	return statusError(client.ErrorInvalidArgument, message, false)
}

func unavailableError(message string, retryable bool) error {
	return statusError(client.ErrorUnavailable, message, retryable)
}

func reconnectWaiterOverload() error {
	facts, err := client.NewErrorFacts(
		"same-client reconnect waiter capacity is exhausted",
		true,
		nil,
	)
	if err != nil {
		return protocolError()
	}
	return constructed(client.NewOverloadError(
		facts,
		"client-reconnect-waiters",
		maximumReconnectWaitersPerClient,
		maximumReconnectWaitersPerClient+1,
	))
}

func statusError(code client.ErrorCode, message string, retryable bool) error {
	facts, err := client.NewErrorFacts(message, retryable, nil)
	if err != nil {
		return errors.New("gRPC client status construction failed")
	}
	return constructed(client.NewStatusError(code, facts))
}

func constructed[T error](value T, err error) error {
	if err != nil {
		return protocolError()
	}
	return value
}
