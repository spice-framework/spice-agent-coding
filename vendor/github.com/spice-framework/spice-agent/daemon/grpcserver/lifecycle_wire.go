package grpcserver

import (
	"errors"
	"fmt"
	"slices"

	"github.com/spice-framework/spice-agent/client"
	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
	"google.golang.org/protobuf/proto"
)

func startRequestFromWire(request *enginev1.StartRunRequest) (client.StartRequest, error) {
	if request == nil || len(request.GetInput().GetParts()) != 1 {
		return client.StartRequest{}, errors.New("start input must contain exactly one text part")
	}
	part, ok := request.GetInput().GetParts()[0].GetValue().(*enginev1.ContentPart_Text)
	if !ok {
		return client.StartRequest{}, errors.New("start input must contain exactly one text part")
	}
	operation, err := client.NewOperationID(request.GetClientOperationId())
	if err != nil {
		return client.StartRequest{}, err
	}
	definition, err := client.NewDefinitionRef(request.GetDefinition().GetId(), request.GetDefinition().GetRevision())
	if err != nil {
		return client.StartRequest{}, err
	}
	input, err := client.NewInput(request.GetInput().GetId(), part.Text)
	if err != nil {
		return client.StartRequest{}, err
	}
	return client.NewStartRequest(operation, definition, input)
}

func cancelRequestFromWire(request *enginev1.CancelRunRequest) (client.CancelRequest, error) {
	run, operation, err := runOperationFromWire(request.GetRunId(), request.GetClientOperationId())
	if err != nil {
		return client.CancelRequest{}, err
	}
	return client.NewCancelRequest(run, operation, request.GetReason())
}

func respondRequestFromWire(request *enginev1.RespondInteractionRequest) (client.RespondRequest, error) {
	run, operation, err := runOperationFromWire(request.GetRunId(), request.GetClientOperationId())
	if err != nil {
		return client.RespondRequest{}, err
	}
	value, err := client.ParseStructuredValue(request.GetValueJson())
	if err != nil {
		return client.RespondRequest{}, err
	}
	response, err := client.NewInteractionResponse(request.GetInteractionId(), value)
	if err != nil {
		return client.RespondRequest{}, err
	}
	return client.NewRespondRequest(run, operation, response)
}

func runMutationFromWire(runID, operationID string) (client.RunMutation, error) {
	run, operation, err := runOperationFromWire(runID, operationID)
	if err != nil {
		return client.RunMutation{}, err
	}
	return client.NewRunMutation(run, operation)
}

func importRequestFromWire(request *enginev1.ImportSnapshotRequest) (client.ImportRequest, error) {
	operation, err := client.NewOperationID(request.GetClientOperationId())
	if err != nil {
		return client.ImportRequest{}, err
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(request.GetSnapshot())
	if err != nil {
		return client.ImportRequest{}, fmt.Errorf("encode snapshot envelope: %w", err)
	}
	snapshot, err := client.ParseSnapshot(encoded)
	if err != nil {
		return client.ImportRequest{}, err
	}
	return client.NewImportRequest(operation, snapshot)
}

func snapshotToWire(snapshot client.Snapshot) (*enginev1.SnapshotEnvelope, error) {
	encoded, err := snapshot.MarshalBinary()
	if err != nil {
		return nil, err
	}
	value := new(enginev1.SnapshotEnvelope)
	if err = proto.Unmarshal(encoded, value); err != nil {
		return nil, errors.New("daemon snapshot is not a valid envelope")
	}
	if err = enginev1.ValidateSnapshotEnvelope(value); err != nil {
		return nil, fmt.Errorf("daemon snapshot envelope: %w", err)
	}
	return value, nil
}

func runOperationFromWire(runID, operationID string) (client.RunRef, client.OperationID, error) {
	run, err := client.NewRunRef(runID)
	if err != nil {
		return client.RunRef{}, client.OperationID{}, err
	}
	operation, err := client.NewOperationID(operationID)
	if err != nil {
		return client.RunRef{}, client.OperationID{}, err
	}
	return run, operation, nil
}

func snapshotsNegotiated(negotiated negotiatedSession) bool {
	response := negotiated.response
	if response == nil || response.GetProtocol().GetMajor() != commonv1.ProtocolMajor ||
		response.GetProtocol().GetMinor() < 1 {
		return false
	}
	names := response.GetCapabilities().GetNames()
	_, snapshots := slices.BinarySearch(names, "snapshots")
	_, authority := slices.BinarySearch(names, enginev1.CapabilitySnapshotAuthorityV1)
	return snapshots && authority
}

func snapshotCapabilityStatus(negotiated negotiatedSession) *commonv1.Status {
	var available []string
	if negotiated.response != nil {
		available = negotiated.response.GetCapabilities().GetNames()
	}
	missing := make([]string, 0, 2)
	for _, required := range []string{enginev1.CapabilitySnapshotAuthorityV1, "snapshots"} {
		if _, found := slices.BinarySearch(available, required); !found {
			missing = append(missing, required)
		}
	}
	if len(missing) == 0 {
		missing = append(missing, enginev1.CapabilitySnapshotAuthorityV1)
	}
	return &commonv1.Status{
		Code:    commonv1.ErrorCode_ERROR_CODE_MISSING_CAPABILITY,
		Message: "snapshot transfer was not negotiated",
		Detail: &commonv1.Status_CapabilityMismatch{CapabilityMismatch: &commonv1.CapabilityMismatch{
			Required:  []string{enginev1.CapabilitySnapshotAuthorityV1, "snapshots"},
			Available: slices.Clone(available), Missing: missing,
		}},
	}
}
