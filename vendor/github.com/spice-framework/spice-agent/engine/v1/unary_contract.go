package enginev1

import (
	"errors"
	"fmt"
	"math"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	"google.golang.org/protobuf/proto"
)

// ValidateHealthRequest validates one initialized-client health query.
func ValidateHealthRequest(request *HealthRequest, limits *commonv1.Limits) error {
	if request == nil {
		return errors.New("health request is required")
	}
	if err := validateClient(request.GetClientId(), request.GetOwnershipEpoch()); err != nil {
		return err
	}
	return validateUnarySize(request, limits)
}

// ValidateHealthResponse validates a bounded health result. A failed response
// contains only its status; a successful response contains the complete
// negotiated server, protocol, and health contract.
func ValidateHealthResponse(response *HealthResponse, limits *commonv1.Limits) error {
	if response == nil {
		return errors.New("health response is required")
	}
	statusErr := validateUnaryStatus(response.GetStatus(), limits)
	if statusErr != nil {
		if response.GetServer() != nil || response.GetProtocol() != nil || response.GetHealth() != nil {
			return errors.New("failed health response contains success fields")
		}
		if err := validateUnarySize(response, limits); err != nil {
			return err
		}
		return statusErr
	}
	if err := commonv1.ValidateBuildIdentity(response.GetServer()); err != nil {
		return fmt.Errorf("health server: %w", err)
	}
	if err := validateNegotiatedProtocol(response.GetProtocol()); err != nil {
		return fmt.Errorf("health protocol: %w", err)
	}
	if err := commonv1.ValidateHealth(response.GetHealth()); err != nil {
		return fmt.Errorf("health summary: %w", err)
	}
	if err := commonv1.ValidateLimits(limits); err != nil {
		return err
	}
	if uint64(len(response.GetHealth().GetDegradedReasons())) > uint64(limits.GetMaxCollectionItems()) {
		return errors.New("health degraded reason count exceeds the negotiated collection limit")
	}
	return commonv1.ValidateEncodedSize(response, limits.GetMaxMessageBytes())
}

// ValidateStartRunResponse validates the stable identity returned by a
// successful start mutation and requires failed responses to carry no result.
func ValidateStartRunResponse(response *StartRunResponse, limits *commonv1.Limits) error {
	if response == nil {
		return errors.New("start run response is required")
	}
	statusErr := validateUnaryStatus(response.GetStatus(), limits)
	if statusErr != nil {
		if response.GetRunId() != "" || response.GetInitialSequence() != 0 ||
			response.GetDuplicateOperation() || response.GetPlanId() != "" {
			return errors.New("failed start run response contains success fields")
		}
		if err := validateUnarySize(response, limits); err != nil {
			return err
		}
		return statusErr
	}
	if err := token("run ID", response.GetRunId(), 128); err != nil {
		return err
	}
	if response.GetInitialSequence() != 1 {
		return errors.New("start run response initial sequence must be one")
	}
	if err := token("plan ID", response.GetPlanId(), maximumTokenBytes); err != nil {
		return err
	}
	return validateUnarySize(response, limits)
}

// ValidateCancelRunResponse validates exactly one successful cancellation
// outcome. A terminal sequence is present only for an already-terminal run.
func ValidateCancelRunResponse(response *CancelRunResponse, limits *commonv1.Limits) error {
	if response == nil {
		return errors.New("cancel run response is required")
	}
	statusErr := validateUnaryStatus(response.GetStatus(), limits)
	if statusErr != nil {
		if response.GetCancellationRequested() || response.GetAlreadyTerminal() || response.GetTerminalSequence() != 0 {
			return errors.New("failed cancel run response contains success fields")
		}
		if err := validateUnarySize(response, limits); err != nil {
			return err
		}
		return statusErr
	}
	if response.GetCancellationRequested() == response.GetAlreadyTerminal() {
		return errors.New("cancel run response requires exactly one cancellation outcome")
	}
	if response.GetAlreadyTerminal() && response.GetTerminalSequence() == 0 {
		return errors.New("already-terminal cancellation requires a terminal sequence")
	}
	if response.GetCancellationRequested() && response.GetTerminalSequence() != 0 {
		return errors.New("new cancellation cannot claim a terminal sequence")
	}
	return validateUnarySize(response, limits)
}

// ValidateRespondInteractionRequest validates a bounded interaction mutation
// without consulting pending runtime state. Correlation is checked separately.
func ValidateRespondInteractionRequest(request *RespondInteractionRequest, limits *commonv1.Limits) error {
	if request == nil {
		return errors.New("respond interaction request is required")
	}
	if err := validateRespondInteractionFields(request); err != nil {
		return err
	}
	return validateUnarySize(request, limits)
}

// ValidateRespondInteractionResponse validates an accepted interaction result.
func ValidateRespondInteractionResponse(response *RespondInteractionResponse, limits *commonv1.Limits) error {
	if response == nil {
		return errors.New("respond interaction response is required")
	}
	statusErr := validateUnaryStatus(response.GetStatus(), limits)
	if statusErr != nil {
		if response.GetAccepted() || response.GetDuplicateOperation() {
			return errors.New("failed interaction response contains success fields")
		}
		if err := validateUnarySize(response, limits); err != nil {
			return err
		}
		return statusErr
	}
	if !response.GetAccepted() {
		return errors.New("successful interaction response must be accepted")
	}
	return validateUnarySize(response, limits)
}

// ValidateSuspendRunResponse validates a committed resumable boundary.
func ValidateSuspendRunResponse(response *SuspendRunResponse, limits *commonv1.Limits) error {
	if response == nil {
		return errors.New("suspend run response is required")
	}
	statusErr := validateUnaryStatus(response.GetStatus(), limits)
	if statusErr != nil {
		if response.GetSuspended() || response.GetBoundarySequence() != 0 || response.GetDuplicateOperation() {
			return errors.New("failed suspend run response contains success fields")
		}
		if err := validateUnarySize(response, limits); err != nil {
			return err
		}
		return statusErr
	}
	if !response.GetSuspended() || response.GetBoundarySequence() == 0 || response.GetBoundarySequence() == math.MaxUint64 {
		return errors.New("successful suspend response requires a positive resumable boundary")
	}
	return validateUnarySize(response, limits)
}

// ValidateResumeRunResponse validates a committed local continuation.
func ValidateResumeRunResponse(response *ResumeRunResponse, limits *commonv1.Limits) error {
	if response == nil {
		return errors.New("resume run response is required")
	}
	statusErr := validateUnaryStatus(response.GetStatus(), limits)
	if statusErr != nil {
		if response.GetResumed() || response.GetNextSequence() != 0 || response.GetDuplicateOperation() {
			return errors.New("failed resume run response contains success fields")
		}
		if err := validateUnarySize(response, limits); err != nil {
			return err
		}
		return statusErr
	}
	if !response.GetResumed() || response.GetNextSequence() == 0 || response.GetNextSequence() == math.MaxUint64 {
		return errors.New("successful resume response requires a positive usable next sequence")
	}
	return validateUnarySize(response, limits)
}

// ValidateExportSnapshotRequest validates one read-only snapshot export query.
func ValidateExportSnapshotRequest(request *ExportSnapshotRequest, limits *commonv1.Limits) error {
	if request == nil {
		return errors.New("export snapshot request is required")
	}
	if err := validateClient(request.GetClientId(), request.GetOwnershipEpoch()); err != nil {
		return err
	}
	if err := token("run ID", request.GetRunId(), 128); err != nil {
		return err
	}
	return validateUnarySize(request, limits)
}

// ValidateExportSnapshotResponse validates a complete structurally authentic
// envelope. Keyed authority verification remains an import-side obligation.
func ValidateExportSnapshotResponse(response *ExportSnapshotResponse, limits *commonv1.Limits) error {
	if response == nil {
		return errors.New("export snapshot response is required")
	}
	statusErr := validateUnaryStatus(response.GetStatus(), limits)
	if statusErr != nil {
		if response.GetSnapshot() != nil {
			return errors.New("failed export snapshot response contains a snapshot")
		}
		if err := validateUnarySize(response, limits); err != nil {
			return err
		}
		return statusErr
	}
	if err := ValidateSnapshotEnvelope(response.GetSnapshot()); err != nil {
		return err
	}
	return validateUnarySize(response, limits)
}

// ValidateImportSnapshotResponse validates the imported stable run identity
// and its first continuation sequence.
func ValidateImportSnapshotResponse(response *ImportSnapshotResponse, limits *commonv1.Limits) error {
	if response == nil {
		return errors.New("import snapshot response is required")
	}
	statusErr := validateUnaryStatus(response.GetStatus(), limits)
	if statusErr != nil {
		if response.GetRunId() != "" || response.GetNextSequence() != 0 || response.GetDuplicateOperation() {
			return errors.New("failed import snapshot response contains success fields")
		}
		if err := validateUnarySize(response, limits); err != nil {
			return err
		}
		return statusErr
	}
	if err := token("run ID", response.GetRunId(), 128); err != nil {
		return err
	}
	if response.GetNextSequence() == 0 || response.GetNextSequence() == math.MaxUint64 {
		return errors.New("successful import response requires a positive usable next sequence")
	}
	return validateUnarySize(response, limits)
}

func validateUnaryStatus(status *commonv1.Status, limits *commonv1.Limits) error {
	if err := commonv1.ValidateLimits(limits); err != nil {
		return err
	}
	if err := commonv1.ValidateStatus(status); err != nil {
		return err
	}
	if err := validateStatusCollections(status, limits); err != nil {
		return err
	}
	return commonv1.AsError(status)
}

func validateStatusCollections(status *commonv1.Status, limits *commonv1.Limits) error {
	if status == nil || limits == nil {
		return nil
	}
	detail := status.GetCapabilityMismatch()
	if detail == nil {
		return nil
	}
	maximum := uint64(limits.GetMaxCollectionItems())
	if uint64(len(detail.GetRequired())) > maximum || uint64(len(detail.GetAvailable())) > maximum ||
		uint64(len(detail.GetMissing())) > maximum {
		return errors.New("capability-mismatch detail exceeds the negotiated collection limit")
	}
	return nil
}

func validateUnarySize(value proto.Message, limits *commonv1.Limits) error {
	if err := commonv1.ValidateLimits(limits); err != nil {
		return err
	}
	return commonv1.ValidateEncodedSize(value, limits.GetMaxMessageBytes())
}

func validateNegotiatedProtocol(version *commonv1.ProtocolVersion) error {
	selected := &commonv1.ProtocolRange{Minimum: version, Maximum: version}
	if err := commonv1.ValidateProtocolRange(selected); err != nil {
		return err
	}
	negotiated, status := commonv1.NegotiateProtocol(selected, commonv1.SupportedProtocolRange())
	if status.GetCode() != commonv1.ErrorCode_ERROR_CODE_OK || !proto.Equal(negotiated, version) {
		return errors.New("protocol version is outside the supported range")
	}
	return nil
}

func validateRespondInteractionFields(request *RespondInteractionRequest) error {
	if err := validateClientMutation(request.GetClientId(), request.GetOwnershipEpoch(), request.GetClientOperationId()); err != nil {
		return err
	}
	if err := token("run ID", request.GetRunId(), 128); err != nil {
		return err
	}
	if err := token("interaction ID", request.GetInteractionId(), 128); err != nil {
		return err
	}
	return validateBoundedJSON("interaction response", request.GetValueJson(), maximumInteractionBytes)
}
