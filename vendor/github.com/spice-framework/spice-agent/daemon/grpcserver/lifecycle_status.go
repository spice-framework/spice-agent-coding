package grpcserver

import (
	"errors"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	"github.com/spice-framework/spice-agent/daemon"
)

type staleSessionFacts interface {
	error
	ExpectedEpoch() uint64
	ObservedEpoch() uint64
}

type hostCapacityFacts interface {
	error
	Resource() string
	Limit() uint64
	Observed() uint64
}

type sessionCapacityFacts interface {
	error
	Resource() string
	Maximum() int
}

var (
	_ staleSessionFacts    = (*daemon.StaleSessionError)(nil)
	_ hostCapacityFacts    = (*daemon.RunHostCapacityError)(nil)
	_ sessionCapacityFacts = (*daemon.SessionGateCapacityError)(nil)
)

// lifecycleFailureStatus translates the transport-independent RunHost error
// vocabulary into safe application-level protocol status. Callers must first
// pass the error through contextTransportError so cancellation and deadline
// failures remain gRPC transport results.
//
// operationID and operationKind are required only for an uncertain mutation.
// A non-empty operation ID is echoed on other mutation failures so clients can
// correlate the refusal without inspecting transport text.
func lifecycleFailureStatus(err error, operationID, operationKind string) *commonv1.Status {
	result := lifecycleStatusWithoutOperation(err)
	if operationID != "" {
		result.OperationId = operationID
	}
	if errors.Is(err, daemon.ErrRunHostUncertain) {
		result = uncertainLifecycleStatus(operationID, operationKind)
	}
	if commonv1.ValidateStatus(result) != nil {
		return internalLifecycleStatus()
	}
	return result
}

func lifecycleStatusWithoutOperation(err error) *commonv1.Status {
	if stale, ok := errors.AsType[staleSessionFacts](err); ok && errors.Is(err, daemon.ErrStaleSession) {
		return &commonv1.Status{
			Code:    commonv1.ErrorCode_ERROR_CODE_STALE_CLIENT,
			Message: "client ownership epoch is stale",
			Detail: &commonv1.Status_StaleClient{StaleClient: &commonv1.StaleClient{
				ExpectedEpoch: stale.ExpectedEpoch(),
				ObservedEpoch: stale.ObservedEpoch(),
			}},
		}
	}
	if capacity, ok := errors.AsType[hostCapacityFacts](err); ok && errors.Is(err, daemon.ErrRunHostCapacity) {
		return overloadLifecycleStatus(
			"daemon capacity is exhausted", capacity.Resource(), capacity.Limit(), capacity.Observed(),
		)
	}
	if capacity, ok := errors.AsType[sessionCapacityFacts](err); ok && errors.Is(err, daemon.ErrSessionGateCapacity) {
		maximum := capacity.Maximum()
		if maximum > 0 {
			limit := uint64(maximum) // #nosec G115 -- guarded positive int always fits uint64.
			return overloadLifecycleStatus(
				"client session capacity is exhausted", capacity.Resource(), limit, limit+1,
			)
		}
	}

	switch {
	case errors.Is(err, daemon.ErrHostedRunUnavailable):
		return &commonv1.Status{
			Code: commonv1.ErrorCode_ERROR_CODE_NOT_FOUND, Message: "run is unavailable",
		}
	case errors.Is(err, daemon.ErrRunHostClosed), errors.Is(err, daemon.ErrSessionStoreClosed):
		return &commonv1.Status{
			Code: commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE, Message: "daemon is unavailable", Retryable: true,
		}
	case errors.Is(err, daemon.ErrRunHostUnavailable):
		return &commonv1.Status{
			Code: commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE, Message: "daemon dependency is unavailable", Retryable: true,
		}
	case errors.Is(err, daemon.ErrRunHostState):
		return &commonv1.Status{
			Code: commonv1.ErrorCode_ERROR_CODE_CONFLICT, Message: "run lifecycle transition conflicts with current state",
		}
	case errors.Is(err, daemon.ErrStaleSession), errors.Is(err, daemon.ErrRunHostCapacity),
		errors.Is(err, daemon.ErrSessionGateCapacity):
		// Recovery-bearing status codes require complete typed facts. A bare
		// sentinel cannot safely invent them, so keep the refusal retryable and
		// intentionally opaque.
		return &commonv1.Status{
			Code: commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE, Message: "daemon operation is temporarily unavailable", Retryable: true,
		}
	default:
		return internalLifecycleStatus()
	}
}

func uncertainLifecycleStatus(operationID, operationKind string) *commonv1.Status {
	return &commonv1.Status{
		Code:        commonv1.ErrorCode_ERROR_CODE_UNCERTAIN_OPERATION,
		Message:     "operation outcome is uncertain",
		OperationId: operationID,
		Detail: &commonv1.Status_UncertainOperation{UncertainOperation: &commonv1.UncertainOperation{
			OperationId: operationID, OperationKind: operationKind,
		}},
	}
}

func overloadLifecycleStatus(message, resource string, limit, observed uint64) *commonv1.Status {
	return &commonv1.Status{
		Code: commonv1.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED, Message: message, Retryable: true,
		Detail: &commonv1.Status_Overload{Overload: &commonv1.Overload{
			Resource: resource, Limit: limit, Observed: observed,
		}},
	}
}

func internalLifecycleStatus() *commonv1.Status {
	return &commonv1.Status{
		Code: commonv1.ErrorCode_ERROR_CODE_INTERNAL, Message: "daemon operation failed",
	}
}

var (
	_ staleSessionFacts    = (*daemon.StaleSessionError)(nil)
	_ hostCapacityFacts    = (*daemon.RunHostCapacityError)(nil)
	_ sessionCapacityFacts = (*daemon.SessionGateCapacityError)(nil)
)
