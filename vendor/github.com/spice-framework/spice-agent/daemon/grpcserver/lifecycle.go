package grpcserver

import (
	"context"

	"github.com/spice-framework/spice-agent/client"
	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
)

func (service *engineService) StartRun(
	ctx context.Context,
	request *enginev1.StartRunRequest,
) (*enginev1.StartRunResponse, error) {
	if err := service.requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if !protocolValid(enginev1.ValidateStartRunRequest(request, service.limits)) {
		return &enginev1.StartRunResponse{Status: invalidLifecycleRequest("start run request is invalid")}, nil
	}
	negotiated, failure := service.lifecycleSession(request.GetClientId(), request.GetOwnershipEpoch())
	if failure != nil {
		return &enginev1.StartRunResponse{Status: failure}, nil
	}
	if !protocolValid(enginev1.ValidateStartRunRequest(request, negotiated.response.GetLimits())) {
		return &enginev1.StartRunResponse{Status: invalidLifecycleRequest("start run request exceeds negotiated limits")}, nil
	}
	value, translated := protocolValue(startRequestFromWire(request))
	if !translated {
		return &enginev1.StartRunResponse{Status: invalidLifecycleRequest("start run input is unsupported")}, nil
	}
	result, err := service.host.Start(ctx, negotiated.session, value)
	if err != nil {
		return lifecycleContextOrStartFailure(ctx, err, request.GetClientOperationId())
	}
	response := &enginev1.StartRunResponse{
		Status: commonv1.OKStatus(), RunId: result.Run().ID(), InitialSequence: result.InitialSequence(),
		DuplicateOperation: result.DuplicateOperation(), PlanId: result.PlanID(),
	}
	if !protocolValid(enginev1.ValidateStartRunResponse(response, negotiated.response.GetLimits())) {
		return &enginev1.StartRunResponse{Status: internalLifecycleFailure("start run result is invalid")}, nil
	}
	return response, nil
}

func (service *engineService) CancelRun(
	ctx context.Context,
	request *enginev1.CancelRunRequest,
) (*enginev1.CancelRunResponse, error) {
	if err := service.requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if !protocolValid(enginev1.ValidateCancelRunRequest(request, service.limits)) {
		return &enginev1.CancelRunResponse{Status: invalidLifecycleRequest("cancel run request is invalid")}, nil
	}
	negotiated, failure := service.lifecycleSession(request.GetClientId(), request.GetOwnershipEpoch())
	if failure != nil {
		return &enginev1.CancelRunResponse{Status: failure}, nil
	}
	if !protocolValid(enginev1.ValidateCancelRunRequest(request, negotiated.response.GetLimits())) {
		return &enginev1.CancelRunResponse{Status: invalidLifecycleRequest("cancel run request exceeds negotiated limits")}, nil
	}
	value, translated := protocolValue(cancelRequestFromWire(request))
	if !translated {
		return &enginev1.CancelRunResponse{Status: invalidLifecycleRequest("cancel run request is invalid")}, nil
	}
	result, err := service.host.Cancel(ctx, negotiated.session, value)
	if err != nil {
		if transportErr := contextTransportError(ctx, err); transportErr != nil {
			return nil, transportErr
		}
		return &enginev1.CancelRunResponse{Status: lifecycleFailureStatus(err, request.GetClientOperationId(), "cancel")}, nil
	}
	response := &enginev1.CancelRunResponse{
		Status: commonv1.OKStatus(), CancellationRequested: result.Requested(),
		AlreadyTerminal: result.AlreadyTerminal(), TerminalSequence: result.TerminalSequence(),
	}
	if !protocolValid(enginev1.ValidateCancelRunResponse(response, negotiated.response.GetLimits())) {
		return &enginev1.CancelRunResponse{Status: internalLifecycleFailure("cancel run result is invalid")}, nil
	}
	return response, nil
}

func (service *engineService) RespondInteraction(
	ctx context.Context,
	request *enginev1.RespondInteractionRequest,
) (*enginev1.RespondInteractionResponse, error) {
	if err := service.requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if !protocolValid(enginev1.ValidateRespondInteractionRequest(request, service.limits)) {
		return &enginev1.RespondInteractionResponse{Status: invalidLifecycleRequest("interaction response is invalid")}, nil
	}
	negotiated, failure := service.lifecycleSession(request.GetClientId(), request.GetOwnershipEpoch())
	if failure != nil {
		return &enginev1.RespondInteractionResponse{Status: failure}, nil
	}
	if !protocolValid(enginev1.ValidateRespondInteractionRequest(request, negotiated.response.GetLimits())) {
		return &enginev1.RespondInteractionResponse{Status: invalidLifecycleRequest("interaction response exceeds negotiated limits")}, nil
	}
	value, translated := protocolValue(respondRequestFromWire(request))
	if !translated {
		return &enginev1.RespondInteractionResponse{Status: invalidLifecycleRequest("interaction response is invalid")}, nil
	}
	result, err := service.host.Respond(ctx, negotiated.session, value)
	if err != nil {
		if transportErr := contextTransportError(ctx, err); transportErr != nil {
			return nil, transportErr
		}
		return &enginev1.RespondInteractionResponse{Status: lifecycleFailureStatus(err, request.GetClientOperationId(), "respond")}, nil
	}
	response := &enginev1.RespondInteractionResponse{
		Status: commonv1.OKStatus(), Accepted: result.Accepted(), DuplicateOperation: result.DuplicateOperation(),
	}
	if !protocolValid(enginev1.ValidateRespondInteractionResponse(response, negotiated.response.GetLimits())) {
		return &enginev1.RespondInteractionResponse{Status: internalLifecycleFailure("interaction result is invalid")}, nil
	}
	return response, nil
}

func (service *engineService) SuspendRun(
	ctx context.Context,
	request *enginev1.SuspendRunRequest,
) (*enginev1.SuspendRunResponse, error) {
	return service.runSuspension(ctx, request)
}

func (service *engineService) ResumeRun(
	ctx context.Context,
	request *enginev1.ResumeRunRequest,
) (*enginev1.ResumeRunResponse, error) {
	if err := service.requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if !protocolValid(enginev1.ValidateResumeRunRequest(request, service.limits)) {
		return &enginev1.ResumeRunResponse{Status: invalidLifecycleRequest("resume run request is invalid")}, nil
	}
	negotiated, failure := service.lifecycleSession(request.GetClientId(), request.GetOwnershipEpoch())
	if failure != nil {
		return &enginev1.ResumeRunResponse{Status: failure}, nil
	}
	if !protocolValid(enginev1.ValidateResumeRunRequest(request, negotiated.response.GetLimits())) {
		return &enginev1.ResumeRunResponse{Status: invalidLifecycleRequest("resume run request exceeds negotiated limits")}, nil
	}
	value, translated := protocolValue(runMutationFromWire(request.GetRunId(), request.GetClientOperationId()))
	if !translated {
		return &enginev1.ResumeRunResponse{Status: invalidLifecycleRequest("resume run request is invalid")}, nil
	}
	result, err := service.host.Resume(ctx, negotiated.session, value)
	if err != nil {
		if transportErr := contextTransportError(ctx, err); transportErr != nil {
			return nil, transportErr
		}
		return &enginev1.ResumeRunResponse{Status: lifecycleFailureStatus(err, request.GetClientOperationId(), "resume")}, nil
	}
	response := &enginev1.ResumeRunResponse{
		Status: commonv1.OKStatus(), Resumed: result.Resumed(), NextSequence: result.NextSequence(),
		DuplicateOperation: result.DuplicateOperation(),
	}
	if !protocolValid(enginev1.ValidateResumeRunResponse(response, negotiated.response.GetLimits())) {
		return &enginev1.ResumeRunResponse{Status: internalLifecycleFailure("resume run result is invalid")}, nil
	}
	return response, nil
}

func (service *engineService) ExportSnapshot(
	ctx context.Context,
	request *enginev1.ExportSnapshotRequest,
) (*enginev1.ExportSnapshotResponse, error) {
	if err := service.requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if !protocolValid(enginev1.ValidateExportSnapshotRequest(request, service.limits)) {
		return &enginev1.ExportSnapshotResponse{Status: invalidLifecycleRequest("snapshot export request is invalid")}, nil
	}
	negotiated, failure := service.lifecycleSession(request.GetClientId(), request.GetOwnershipEpoch())
	if failure != nil {
		return &enginev1.ExportSnapshotResponse{Status: failure}, nil
	}
	if !snapshotsNegotiated(negotiated) {
		return &enginev1.ExportSnapshotResponse{Status: snapshotCapabilityStatus(negotiated)}, nil
	}
	if !protocolValid(enginev1.ValidateExportSnapshotRequest(request, negotiated.response.GetLimits())) {
		return &enginev1.ExportSnapshotResponse{Status: invalidLifecycleRequest("snapshot export request exceeds negotiated limits")}, nil
	}
	run, translated := protocolValue(client.NewRunRef(request.GetRunId()))
	if !translated {
		return &enginev1.ExportSnapshotResponse{Status: invalidLifecycleRequest("snapshot export request is invalid")}, nil
	}
	snapshot, err := service.host.Export(ctx, negotiated.session, run)
	if err != nil {
		if transportErr := contextTransportError(ctx, err); transportErr != nil {
			return nil, transportErr
		}
		return &enginev1.ExportSnapshotResponse{Status: lifecycleFailureStatus(err, "", "export")}, nil
	}
	wire, translated := protocolValue(snapshotToWire(snapshot))
	if !translated {
		return &enginev1.ExportSnapshotResponse{Status: internalLifecycleFailure("snapshot export result is invalid")}, nil
	}
	response := &enginev1.ExportSnapshotResponse{Status: commonv1.OKStatus(), Snapshot: wire}
	if !protocolValid(enginev1.ValidateExportSnapshotResponse(response, negotiated.response.GetLimits())) {
		return &enginev1.ExportSnapshotResponse{Status: internalLifecycleFailure("snapshot export result is invalid")}, nil
	}
	return response, nil
}

func (service *engineService) ImportSnapshot(
	ctx context.Context,
	request *enginev1.ImportSnapshotRequest,
) (*enginev1.ImportSnapshotResponse, error) {
	if err := service.requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if !protocolValid(enginev1.ValidateImportSnapshotRequestStructure(request, service.limits)) {
		return &enginev1.ImportSnapshotResponse{Status: invalidLifecycleRequest("snapshot import request is invalid")}, nil
	}
	negotiated, failure := service.lifecycleSession(request.GetClientId(), request.GetOwnershipEpoch())
	if failure != nil {
		return &enginev1.ImportSnapshotResponse{Status: failure}, nil
	}
	if !snapshotsNegotiated(negotiated) {
		return &enginev1.ImportSnapshotResponse{Status: snapshotCapabilityStatus(negotiated)}, nil
	}
	if !protocolValid(enginev1.ValidateImportSnapshotRequestStructure(request, negotiated.response.GetLimits())) {
		return &enginev1.ImportSnapshotResponse{Status: invalidLifecycleRequest("snapshot import request exceeds negotiated limits")}, nil
	}
	value, translated := protocolValue(importRequestFromWire(request))
	if !translated {
		return &enginev1.ImportSnapshotResponse{Status: invalidLifecycleRequest("snapshot import request is invalid")}, nil
	}
	result, err := service.host.Import(ctx, negotiated.session, value)
	if err != nil {
		if transportErr := contextTransportError(ctx, err); transportErr != nil {
			return nil, transportErr
		}
		return &enginev1.ImportSnapshotResponse{Status: lifecycleFailureStatus(err, request.GetClientOperationId(), "import")}, nil
	}
	response := &enginev1.ImportSnapshotResponse{
		Status: commonv1.OKStatus(), RunId: result.Run().ID(), NextSequence: result.NextSequence(),
		DuplicateOperation: result.DuplicateOperation(),
	}
	if !protocolValid(enginev1.ValidateImportSnapshotResponse(response, negotiated.response.GetLimits())) {
		return &enginev1.ImportSnapshotResponse{Status: internalLifecycleFailure("snapshot import result is invalid")}, nil
	}
	return response, nil
}

func (service *engineService) runSuspension(
	ctx context.Context,
	request *enginev1.SuspendRunRequest,
) (*enginev1.SuspendRunResponse, error) {
	if err := service.requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if !protocolValid(enginev1.ValidateSuspendRunRequest(request, service.limits)) {
		return &enginev1.SuspendRunResponse{Status: invalidLifecycleRequest("suspend run request is invalid")}, nil
	}
	negotiated, failure := service.lifecycleSession(request.GetClientId(), request.GetOwnershipEpoch())
	if failure != nil {
		return &enginev1.SuspendRunResponse{Status: failure}, nil
	}
	if !protocolValid(enginev1.ValidateSuspendRunRequest(request, negotiated.response.GetLimits())) {
		return &enginev1.SuspendRunResponse{Status: invalidLifecycleRequest("suspend run request exceeds negotiated limits")}, nil
	}
	value, translated := protocolValue(runMutationFromWire(request.GetRunId(), request.GetClientOperationId()))
	if !translated {
		return &enginev1.SuspendRunResponse{Status: invalidLifecycleRequest("suspend run request is invalid")}, nil
	}
	result, err := service.host.Suspend(ctx, negotiated.session, value)
	if err != nil {
		if transportErr := contextTransportError(ctx, err); transportErr != nil {
			return nil, transportErr
		}
		return &enginev1.SuspendRunResponse{Status: lifecycleFailureStatus(err, request.GetClientOperationId(), "suspend")}, nil
	}
	response := &enginev1.SuspendRunResponse{
		Status: commonv1.OKStatus(), Suspended: result.Suspended(), BoundarySequence: result.BoundarySequence(),
		DuplicateOperation: result.DuplicateOperation(),
	}
	if !protocolValid(enginev1.ValidateSuspendRunResponse(response, negotiated.response.GetLimits())) {
		return &enginev1.SuspendRunResponse{Status: internalLifecycleFailure("suspend run result is invalid")}, nil
	}
	return response, nil
}

func (service *engineService) lifecycleSession(clientID string, epoch uint64) (negotiatedSession, *commonv1.Status) {
	negotiated, err := service.registry.lookup(clientID, epoch)
	if err != nil {
		if checkErr := service.sessions.Check(clientID, epoch); checkErr != nil {
			return negotiatedSession{}, sessionFailureStatus(checkErr)
		}
		return negotiatedSession{}, &commonv1.Status{
			Code:    commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE,
			Message: "client session is unavailable", Retryable: true,
		}
	}
	if err = service.sessions.Check(negotiated.session.ClientID(), negotiated.session.Epoch()); err != nil {
		return negotiatedSession{}, sessionFailureStatus(err)
	}
	return negotiated, nil
}

func lifecycleContextOrStartFailure(
	ctx context.Context,
	err error,
	operationID string,
) (*enginev1.StartRunResponse, error) {
	if transportErr := contextTransportError(ctx, err); transportErr != nil {
		return nil, transportErr
	}
	return &enginev1.StartRunResponse{Status: lifecycleFailureStatus(err, operationID, "start")}, nil
}

func invalidLifecycleRequest(message string) *commonv1.Status {
	return &commonv1.Status{Code: commonv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: message}
}

func internalLifecycleFailure(message string) *commonv1.Status {
	return &commonv1.Status{Code: commonv1.ErrorCode_ERROR_CODE_INTERNAL, Message: message}
}

// protocolValid explicitly discards private validator detail at the adapter
// boundary. Callers return one fixed application status rather than exposing
// the validation error through gRPC or response text.
func protocolValid(err error) bool { return err == nil }

func protocolValue[T any](value T, err error) (T, bool) { return value, err == nil }

var _ runHostBoundary = runHostAdapter{}
