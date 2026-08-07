package grpcclient

import (
	"context"

	"github.com/spice-framework/spice-agent/client"
	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
)

func (current *session) Start(ctx context.Context, request client.StartRequest) (client.StartResult, error) {
	if err := request.Validate(); err != nil {
		return client.StartResult{}, invalidArgumentError("start request is invalid")
	}
	rpcContext, err := current.rpcContext(ctx)
	if err != nil {
		return client.StartResult{}, err
	}
	wireRequest := &enginev1.StartRunRequest{
		ClientId: current.connection.ClientID(), OwnershipEpoch: current.connection.OwnershipEpoch(),
		ClientOperationId: request.Operation().String(),
		Definition: &enginev1.AgentDefinitionRef{
			Id: request.Definition().ID(), Revision: request.Definition().Revision(),
		},
		Input: &enginev1.Message{
			Id: request.Input().MessageID(), Role: enginev1.MessageRole_MESSAGE_ROLE_USER,
			Parts: []*enginev1.ContentPart{{
				Value: &enginev1.ContentPart_Text{Text: request.Input().Text()},
			}},
		},
	}
	limits, _ := limitsToWire(current.connection.Limits())
	if enginev1.ValidateStartRunRequest(wireRequest, limits) != nil {
		return client.StartResult{}, protocolError()
	}
	response, err := current.service.StartRun(rpcContext, wireRequest, current.callOptions()...)
	if err != nil {
		return client.StartResult{}, mutationTransportError(ctx, err, request.Operation(), "start")
	}
	if err = responseError(response.GetStatus(), func() error {
		return enginev1.ValidateStartRunResponse(response, limits)
	}, responseContext{operation: new(request.Operation())}); err != nil {
		return client.StartResult{}, err
	}
	run, err := client.NewRunRef(response.GetRunId())
	if err != nil {
		return client.StartResult{}, protocolError()
	}
	result, err := client.NewStartResult(
		run, response.GetInitialSequence(), response.GetPlanId(), response.GetDuplicateOperation(),
	)
	if err != nil {
		return client.StartResult{}, protocolError()
	}
	return result, nil
}

func (current *session) Cancel(ctx context.Context, request client.CancelRequest) (client.CancelResult, error) {
	if err := request.Run().Validate(); err != nil {
		return client.CancelResult{}, invalidArgumentError("cancel request is invalid")
	}
	if err := request.Operation().Validate(); err != nil {
		return client.CancelResult{}, invalidArgumentError("cancel request is invalid")
	}
	rpcContext, err := current.rpcContext(ctx)
	if err != nil {
		return client.CancelResult{}, err
	}
	wireRequest := &enginev1.CancelRunRequest{
		ClientId: current.connection.ClientID(), OwnershipEpoch: current.connection.OwnershipEpoch(),
		ClientOperationId: request.Operation().String(), RunId: request.Run().ID(), Reason: request.Reason(),
	}
	limits, _ := limitsToWire(current.connection.Limits())
	if enginev1.ValidateCancelRunRequest(wireRequest, limits) != nil {
		return client.CancelResult{}, invalidArgumentError("cancel request is invalid")
	}
	response, err := current.service.CancelRun(rpcContext, wireRequest, current.callOptions()...)
	if err != nil {
		return client.CancelResult{}, mutationTransportError(ctx, err, request.Operation(), "cancel")
	}
	if err = responseError(response.GetStatus(), func() error {
		return enginev1.ValidateCancelRunResponse(response, limits)
	}, responseContext{run: new(request.Run()), operation: new(request.Operation()), sessionEpoch: current.connection.OwnershipEpoch()}); err != nil {
		return client.CancelResult{}, err
	}
	result, err := client.NewCancelResult(
		response.GetCancellationRequested(), response.GetAlreadyTerminal(), response.GetTerminalSequence(),
	)
	if err != nil {
		return client.CancelResult{}, protocolError()
	}
	return result, nil
}

func (current *session) Respond(ctx context.Context, request client.RespondRequest) (client.RespondResult, error) {
	if err := request.Run().Validate(); err != nil {
		return client.RespondResult{}, invalidArgumentError("interaction response is invalid")
	}
	if err := request.Operation().Validate(); err != nil {
		return client.RespondResult{}, invalidArgumentError("interaction response is invalid")
	}
	if err := request.Response().Validate(); err != nil {
		return client.RespondResult{}, invalidArgumentError("interaction response is invalid")
	}
	value, err := request.Response().Value().EncodeTransfer()
	if err != nil {
		return client.RespondResult{}, invalidArgumentError("interaction response is invalid")
	}
	rpcContext, err := current.rpcContext(ctx)
	if err != nil {
		return client.RespondResult{}, err
	}
	wireRequest := &enginev1.RespondInteractionRequest{
		ClientId: current.connection.ClientID(), OwnershipEpoch: current.connection.OwnershipEpoch(),
		ClientOperationId: request.Operation().String(), RunId: request.Run().ID(),
		InteractionId: request.Response().ID(), ValueJson: value,
	}
	limits, _ := limitsToWire(current.connection.Limits())
	if enginev1.ValidateRespondInteractionRequest(wireRequest, limits) != nil {
		return client.RespondResult{}, invalidArgumentError("interaction response is invalid")
	}
	response, err := current.service.RespondInteraction(rpcContext, wireRequest, current.callOptions()...)
	if err != nil {
		return client.RespondResult{}, mutationTransportError(ctx, err, request.Operation(), "respond")
	}
	if err = responseError(response.GetStatus(), func() error {
		return enginev1.ValidateRespondInteractionResponse(response, limits)
	}, responseContext{run: new(request.Run()), operation: new(request.Operation()), sessionEpoch: current.connection.OwnershipEpoch()}); err != nil {
		return client.RespondResult{}, err
	}
	result, err := client.NewRespondResult(response.GetAccepted(), response.GetDuplicateOperation())
	if err != nil {
		return client.RespondResult{}, protocolError()
	}
	return result, nil
}

func (current *session) Suspend(ctx context.Context, request client.RunMutation) (client.SuspendResult, error) {
	return sessionMutation(
		current, ctx, request, "suspend",
		func(rpcContext context.Context, wireRequest *enginev1.SuspendRunRequest) (*enginev1.SuspendRunResponse, error) {
			return current.service.SuspendRun(rpcContext, wireRequest, current.callOptions()...)
		},
	)
}

func (current *session) Resume(ctx context.Context, request client.RunMutation) (client.ResumeResult, error) {
	if err := validateRunMutation(request); err != nil {
		return client.ResumeResult{}, err
	}
	rpcContext, err := current.rpcContext(ctx)
	if err != nil {
		return client.ResumeResult{}, err
	}
	wireRequest := &enginev1.ResumeRunRequest{
		ClientId: current.connection.ClientID(), OwnershipEpoch: current.connection.OwnershipEpoch(),
		ClientOperationId: request.Operation().String(), RunId: request.Run().ID(),
	}
	limits, _ := limitsToWire(current.connection.Limits())
	if enginev1.ValidateResumeRunRequest(wireRequest, limits) != nil {
		return client.ResumeResult{}, protocolError()
	}
	response, err := current.service.ResumeRun(rpcContext, wireRequest, current.callOptions()...)
	if err != nil {
		return client.ResumeResult{}, mutationTransportError(ctx, err, request.Operation(), "resume")
	}
	if err = responseError(response.GetStatus(), func() error {
		return enginev1.ValidateResumeRunResponse(response, limits)
	}, responseContext{run: new(request.Run()), operation: new(request.Operation()), sessionEpoch: current.connection.OwnershipEpoch()}); err != nil {
		return client.ResumeResult{}, err
	}
	result, err := client.NewResumeResult(
		response.GetResumed(), response.GetNextSequence(), response.GetDuplicateOperation(),
	)
	if err != nil {
		return client.ResumeResult{}, protocolError()
	}
	return result, nil
}

func sessionMutation(
	current *session,
	ctx context.Context,
	request client.RunMutation,
	kind string,
	call func(context.Context, *enginev1.SuspendRunRequest) (*enginev1.SuspendRunResponse, error),
) (client.SuspendResult, error) {
	if err := validateRunMutation(request); err != nil {
		return client.SuspendResult{}, err
	}
	rpcContext, err := current.rpcContext(ctx)
	if err != nil {
		return client.SuspendResult{}, err
	}
	wireRequest := &enginev1.SuspendRunRequest{
		ClientId: current.connection.ClientID(), OwnershipEpoch: current.connection.OwnershipEpoch(),
		ClientOperationId: request.Operation().String(), RunId: request.Run().ID(),
	}
	limits, _ := limitsToWire(current.connection.Limits())
	if enginev1.ValidateSuspendRunRequest(wireRequest, limits) != nil {
		return client.SuspendResult{}, protocolError()
	}
	response, err := call(rpcContext, wireRequest)
	if err != nil {
		return client.SuspendResult{}, mutationTransportError(ctx, err, request.Operation(), kind)
	}
	if err = responseError(response.GetStatus(), func() error {
		return enginev1.ValidateSuspendRunResponse(response, limits)
	}, responseContext{run: new(request.Run()), operation: new(request.Operation()), sessionEpoch: current.connection.OwnershipEpoch()}); err != nil {
		return client.SuspendResult{}, err
	}
	result, err := client.NewSuspendResult(
		response.GetSuspended(), response.GetBoundarySequence(), response.GetDuplicateOperation(),
	)
	if err != nil {
		return client.SuspendResult{}, protocolError()
	}
	return result, nil
}

func (current *session) Export(ctx context.Context, run client.RunRef) (client.Snapshot, error) {
	if err := run.Validate(); err != nil {
		return client.Snapshot{}, invalidArgumentError("snapshot export run is invalid")
	}
	rpcContext, err := current.rpcContext(ctx)
	if err != nil {
		return client.Snapshot{}, err
	}
	wireRequest := &enginev1.ExportSnapshotRequest{
		ClientId: current.connection.ClientID(), OwnershipEpoch: current.connection.OwnershipEpoch(), RunId: run.ID(),
	}
	limits, _ := limitsToWire(current.connection.Limits())
	if enginev1.ValidateExportSnapshotRequest(wireRequest, limits) != nil {
		return client.Snapshot{}, protocolError()
	}
	response, err := current.service.ExportSnapshot(rpcContext, wireRequest, current.callOptions()...)
	if err != nil {
		return client.Snapshot{}, transportError(ctx, err)
	}
	if err = responseError(response.GetStatus(), func() error {
		return enginev1.ValidateExportSnapshotResponse(response, limits)
	}, responseContext{run: new(run), readOnly: true, sessionEpoch: current.connection.OwnershipEpoch()}); err != nil {
		return client.Snapshot{}, err
	}
	result, err := snapshotFromWire(response.GetSnapshot())
	if err != nil {
		return client.Snapshot{}, protocolError()
	}
	return result, nil
}

func (current *session) Import(ctx context.Context, request client.ImportRequest) (client.ImportResult, error) {
	if err := request.Operation().Validate(); err != nil {
		return client.ImportResult{}, invalidArgumentError("snapshot import is invalid")
	}
	snapshot, err := snapshotToWire(request.Snapshot())
	if err != nil {
		return client.ImportResult{}, invalidArgumentError("snapshot import is invalid")
	}
	rpcContext, err := current.rpcContext(ctx)
	if err != nil {
		return client.ImportResult{}, err
	}
	wireRequest := &enginev1.ImportSnapshotRequest{
		ClientId: current.connection.ClientID(), OwnershipEpoch: current.connection.OwnershipEpoch(),
		ClientOperationId: request.Operation().String(), Snapshot: snapshot,
	}
	limits, _ := limitsToWire(current.connection.Limits())
	if enginev1.ValidateImportSnapshotRequestStructure(wireRequest, limits) != nil {
		return client.ImportResult{}, protocolError()
	}
	response, err := current.service.ImportSnapshot(rpcContext, wireRequest, current.callOptions()...)
	if err != nil {
		return client.ImportResult{}, mutationTransportError(ctx, err, request.Operation(), "import")
	}
	if err = responseError(response.GetStatus(), func() error {
		return enginev1.ValidateImportSnapshotResponse(response, limits)
	}, responseContext{operation: new(request.Operation()), sessionEpoch: current.connection.OwnershipEpoch()}); err != nil {
		return client.ImportResult{}, err
	}
	run, err := client.NewRunRef(response.GetRunId())
	if err != nil {
		return client.ImportResult{}, protocolError()
	}
	result, err := client.NewImportResult(run, response.GetNextSequence(), response.GetDuplicateOperation())
	if err != nil {
		return client.ImportResult{}, protocolError()
	}
	return result, nil
}

func (current *session) Health(ctx context.Context) (client.Health, error) {
	rpcContext, err := current.rpcContext(ctx)
	if err != nil {
		return client.Health{}, err
	}
	request := &enginev1.HealthRequest{
		ClientId: current.connection.ClientID(), OwnershipEpoch: current.connection.OwnershipEpoch(),
	}
	limits, _ := limitsToWire(current.connection.Limits())
	if enginev1.ValidateHealthRequest(request, limits) != nil {
		return client.Health{}, protocolError()
	}
	response, err := current.service.Health(rpcContext, request, current.callOptions()...)
	if err != nil {
		return client.Health{}, transportError(ctx, err)
	}
	if err = responseError(response.GetStatus(), func() error {
		return enginev1.ValidateHealthResponse(response, limits)
	}, responseContext{readOnly: true, sessionEpoch: current.connection.OwnershipEpoch()}); err != nil {
		return client.Health{}, err
	}
	server, err := buildFromWire(response.GetServer())
	if err != nil || server != current.connection.Server() {
		return client.Health{}, protocolError()
	}
	protocol, err := protocolVersionFromWire(response.GetProtocol())
	if err != nil || protocol != current.connection.Protocol() {
		return client.Health{}, protocolError()
	}
	health, err := healthFromWire(response.GetHealth())
	if err != nil {
		return client.Health{}, protocolError()
	}
	return health, nil
}

func validateRunMutation(request client.RunMutation) error {
	if err := request.Run().Validate(); err != nil {
		return invalidArgumentError("run mutation is invalid")
	}
	if err := request.Operation().Validate(); err != nil {
		return invalidArgumentError("run mutation is invalid")
	}
	return nil
}

type responseContext struct {
	run          *client.RunRef
	operation    *client.OperationID
	sessionEpoch uint64
	readOnly     bool
}

func responseError(status *commonv1.Status, validate func() error, expected responseContext) error {
	err := validate()
	if err == nil {
		return nil
	}
	statusErr := validatedStatusError(err)
	if statusErr == nil {
		return protocolError()
	}
	if statusErr.Status().GetCode() != status.GetCode() {
		return protocolError()
	}
	return statusToError(statusErr.Status(), statusContext{
		run: expected.run, operation: expected.operation,
		sessionEpoch: expected.sessionEpoch, readOnly: expected.readOnly,
	})
}
