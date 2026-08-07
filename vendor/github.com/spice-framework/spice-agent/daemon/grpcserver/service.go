package grpcserver

import (
	"context"
	"errors"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	"github.com/spice-framework/spice-agent/daemon"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type engineService struct {
	enginev1.UnimplementedEngineServiceServer

	root         context.Context //nolint:containedctx // adapter service lifetime, never an RPC lifetime.
	host         runHostBoundary
	sessions     sessionStoreBoundary
	registry     *negotiatedSessionRegistry
	build        *commonv1.BuildIdentity
	capabilities *commonv1.CapabilitySet
	limits       *commonv1.Limits
}

func (service *engineService) Initialize(
	ctx context.Context,
	request *enginev1.InitializeRequest,
) (*enginev1.InitializeResponse, error) {
	if err := service.requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	var attempt *initializationAttemptLease
	// Invalid protocol-1.3 requests stay on the pure PreflightInitialize error
	// path and never consume replay-ledger capacity. A previously committed
	// exact request is still replayed before mutable host description work.
	if len(request.GetInitializationAttemptId()) != 0 && enginev1.ValidateInitializeRequest(request) == nil {
		fingerprint, err := fingerprintInitializeRequest(request)
		if err != nil {
			return initializeContextOrFailure(ctx, err)
		}
		var replay *enginev1.InitializeResponse
		attempt, replay, err = service.registry.reserveInitializationAttempt(
			ctx, request.GetInitializationAttemptId(), fingerprint,
		)
		if err != nil {
			return initializeContextOrFailure(ctx, err)
		}
		if replay != nil {
			return replay, nil
		}
		defer attempt.abort()
	}
	description, err := service.host.Describe(ctx)
	if err != nil {
		return initializeContextOrFailure(ctx, err)
	}
	health, err := healthToWire(description.Health())
	if err != nil {
		return initializeInternalFailure("daemon health is invalid"), nil
	}
	definitions, err := definitionsToWire(description.Definitions(), service.limits)
	if err != nil {
		return initializeInternalFailure("daemon definitions are invalid"), nil
	}
	negotiation, failure := enginev1.PreflightInitialize(
		request,
		commonv1.SupportedProtocolRange(),
		proto.CloneOf(service.build),
		proto.CloneOf(service.capabilities),
		proto.CloneOf(service.limits),
		health,
		definitions,
	)
	if failure != nil {
		return failure, nil
	}

	claim := negotiation.ReconnectClaim()
	if claim == nil {
		response, transactionErr := service.registry.initializeFreshAttempt(ctx, attempt, func() (
			daemon.Session,
			*enginev1.InitializeResponse,
			error,
		) {
			fresh, freshErr := service.sessions.Fresh()
			if freshErr != nil {
				return daemon.Session{}, nil, freshErr
			}
			return fresh, enginev1.CompleteInitialize(negotiation, fresh.ClientID(), fresh.Epoch()), nil
		})
		if transactionErr != nil {
			return initializeContextOrFailure(ctx, transactionErr)
		}
		return proto.CloneOf(response), nil
	} else {
		clientID, expectedEpoch := claim.GetClientId(), claim.GetExpectedOwnershipEpoch()
		response := enginev1.CompleteInitialize(negotiation, clientID, expectedEpoch+1)
		response, transactionErr := service.registry.initializeReconnectAttempt(
			ctx, attempt, clientID, expectedEpoch, response,
			func() (daemon.Session, error) {
				return service.sessions.ReconnectContext(ctx, clientID, expectedEpoch)
			},
		)
		if transactionErr != nil {
			if errors.Is(transactionErr, errNegotiatedSessionUnavailable) {
				if ownershipErr := service.sessions.Check(clientID, expectedEpoch); ownershipErr != nil {
					transactionErr = ownershipErr
				}
			}
			return initializeContextOrFailure(ctx, transactionErr)
		}
		return proto.CloneOf(response), nil
	}
}

func (service *engineService) Health(
	ctx context.Context,
	request *enginev1.HealthRequest,
) (*enginev1.HealthResponse, error) {
	if err := service.requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if err := enginev1.ValidateHealthRequest(request, service.limits); err != nil {
		//nolint:nilerr // request validation failures are application statuses, not transport failures.
		return healthFailure(commonv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "health request is invalid"), nil
	}
	negotiated, err := service.registry.lookup(request.GetClientId(), request.GetOwnershipEpoch())
	if err != nil {
		if checkErr := service.sessions.Check(request.GetClientId(), request.GetOwnershipEpoch()); checkErr != nil {
			return healthFailureForSession(checkErr), nil
		}
		return healthFailure(commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE, "client session is unavailable"), nil
	}
	if err = service.sessions.Check(negotiated.session.ClientID(), negotiated.session.Epoch()); err != nil {
		return healthFailureForSession(err), nil
	}
	health, err := service.host.Health(ctx, negotiated.session)
	if err != nil {
		if transportErr := contextTransportError(ctx, err); transportErr != nil {
			return nil, transportErr
		}
		return healthFailureForSession(err), nil
	}
	wireHealth, err := healthToWire(health)
	if err != nil {
		//nolint:nilerr // invalid host output is a bounded application status.
		return healthFailure(commonv1.ErrorCode_ERROR_CODE_INTERNAL, "daemon health is invalid"), nil
	}
	response := &enginev1.HealthResponse{
		Status: commonv1.OKStatus(), Server: proto.CloneOf(negotiated.response.GetServer()),
		Protocol: proto.CloneOf(negotiated.response.GetProtocol()), Health: wireHealth,
	}
	if err = enginev1.ValidateHealthResponse(response, negotiated.response.GetLimits()); err != nil {
		//nolint:nilerr // invalid adapter output is a bounded application status.
		return healthFailure(commonv1.ErrorCode_ERROR_CODE_INTERNAL, "health response is invalid"), nil
	}
	return response, nil
}

func (service *engineService) requireAuthenticated(ctx context.Context) error {
	if service == nil || service.root == nil || service.host == nil || service.sessions == nil ||
		service.registry == nil || service.build == nil || service.capabilities == nil || service.limits == nil ||
		!transportAuthenticated(ctx) {
		return unauthenticatedTransport()
	}
	if service.root.Err() != nil {
		return status.Error(codes.Unavailable, "local daemon is stopping")
	}
	return nil
}

func (service *engineService) streamContext(rpcContext context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(rpcContext)
	stopRoot := context.AfterFunc(service.root, cancel)
	return ctx, func() {
		stopRoot()
		cancel()
	}
}

func initializeContextOrFailure(
	ctx context.Context,
	err error,
) (*enginev1.InitializeResponse, error) {
	if transportErr := contextTransportError(ctx, err); transportErr != nil {
		return nil, transportErr
	}
	return initializeFailureForSession(err), nil
}

func contextTransportError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return status.Error(codes.Canceled, context.Canceled.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, context.DeadlineExceeded.Error())
	}
	return nil
}

func initializeFailureForSession(err error) *enginev1.InitializeResponse {
	statusValue := sessionFailureStatus(err)
	return &enginev1.InitializeResponse{Status: statusValue}
}

func initializeInternalFailure(message string) *enginev1.InitializeResponse {
	return &enginev1.InitializeResponse{Status: &commonv1.Status{
		Code: commonv1.ErrorCode_ERROR_CODE_INTERNAL, Message: message,
	}}
}

func healthFailureForSession(err error) *enginev1.HealthResponse {
	return &enginev1.HealthResponse{Status: sessionFailureStatus(err)}
}

func healthFailure(code commonv1.ErrorCode, message string) *enginev1.HealthResponse {
	return &enginev1.HealthResponse{Status: &commonv1.Status{Code: code, Message: message}}
}

func sessionFailureStatus(err error) *commonv1.Status {
	if errors.Is(err, errInitializationAttemptConflict) {
		return &commonv1.Status{
			Code:    commonv1.ErrorCode_ERROR_CODE_CONFLICT,
			Message: "initialization attempt identity conflicts with another request",
		}
	}
	var negotiatedCapacity *negotiatedCapacityError
	if errors.As(err, &negotiatedCapacity) && negotiatedCapacity.limit > 0 {
		return &commonv1.Status{
			Code: commonv1.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED, Message: "client session capacity is exhausted",
			Detail: &commonv1.Status_Overload{Overload: &commonv1.Overload{
				Resource: negotiatedCapacity.resource, Limit: negotiatedCapacity.limit,
				Observed: negotiatedCapacity.limit + 1,
			}},
		}
	}
	var stale *daemon.StaleSessionError
	if errors.As(err, &stale) && stale.ExpectedEpoch() != 0 {
		return &commonv1.Status{
			Code: commonv1.ErrorCode_ERROR_CODE_STALE_CLIENT, Message: "client ownership epoch is stale",
			Detail: &commonv1.Status_StaleClient{StaleClient: &commonv1.StaleClient{
				ExpectedEpoch: stale.ExpectedEpoch(), ObservedEpoch: stale.ObservedEpoch(),
			}},
		}
	}
	var capacity *daemon.SessionGateCapacityError
	if errors.As(err, &capacity) && capacity.Maximum() > 0 {
		return &commonv1.Status{
			Code: commonv1.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED, Message: "client session capacity is exhausted",
			Detail: &commonv1.Status_Overload{Overload: &commonv1.Overload{
				Resource: capacity.Resource(), Limit: uint64(capacity.Maximum()), // #nosec G115 -- maximum is validated positive and bounded.
				Observed: uint64(capacity.Maximum()) + 1, // #nosec G115 -- maximum is validated positive and bounded.
			}},
		}
	}
	return &commonv1.Status{
		Code:    commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE,
		Message: "client session is unavailable", Retryable: true,
	}
}
