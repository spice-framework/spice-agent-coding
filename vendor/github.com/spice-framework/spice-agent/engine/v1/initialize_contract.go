package enginev1

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	"google.golang.org/protobuf/proto"
)

// InitializeNegotiation is the immutable result of pure initialization
// negotiation. A host allocates or reconnects a client session only after this
// value exists, then completes the response with the resulting ownership.
type InitializeNegotiation struct {
	reconnectClaim *ReconnectClaim
	attemptID      []byte
	protocol       *commonv1.ProtocolVersion
	server         *commonv1.BuildIdentity
	capabilities   *commonv1.CapabilitySet
	limits         *commonv1.Limits
	health         *commonv1.Health
	definitions    *DefinitionSet
}

// InitializationAttemptID returns the validated protocol-1.3 attempt
// identity, or nil for a legacy request. The returned bytes are independent.
func (negotiation *InitializeNegotiation) InitializationAttemptID() []byte {
	if negotiation == nil {
		return nil
	}
	return slices.Clone(negotiation.attemptID)
}

// ReconnectClaim returns the validated reconnect claim, or nil for a new
// client. The returned Protobuf value owns independent storage.
func (negotiation *InitializeNegotiation) ReconnectClaim() *ReconnectClaim {
	if negotiation == nil || negotiation.reconnectClaim == nil {
		return nil
	}
	return clone(negotiation.reconnectClaim)
}

// ValidateInitializeRequest applies the fixed bootstrap contract before any
// negotiated session limit or ownership state exists.
func ValidateInitializeRequest(request *InitializeRequest) error {
	if request == nil {
		return errors.New("initialize request is required")
	}
	if err := commonv1.ValidateProtocolRange(request.GetProtocol()); err != nil {
		return err
	}
	if err := validateInitializeRequestAttempt(request.GetProtocol(), request.GetInitializationAttemptId()); err != nil {
		return err
	}
	if err := commonv1.ValidateBuildIdentity(request.GetClient()); err != nil {
		return fmt.Errorf("client build identity: %w", err)
	}
	if err := commonv1.ValidateCapabilities(request.GetSupportedCapabilities()); err != nil {
		return fmt.Errorf("client supported capabilities: %w", err)
	}
	if err := commonv1.ValidateCapabilities(request.GetRequiredCapabilities()); err != nil {
		return fmt.Errorf("client required capabilities: %w", err)
	}
	for _, required := range request.GetRequiredCapabilities().GetNames() {
		if !containsCapability(request.GetSupportedCapabilities(), required) {
			return errors.New("required capabilities must be client-supported")
		}
	}
	if err := commonv1.ValidateLimits(request.GetRequestedLimits()); err != nil {
		return err
	}
	if err := validateReconnectClaim(request.GetReconnectClaim()); err != nil {
		return err
	}
	return commonv1.ValidateEncodedSize(request, InitializeBootstrapMaximumBytes)
}

// PreflightInitialize validates and negotiates every transport-independent
// field without allocating or mutating client-session state. On failure it
// returns an error-only wire response and no negotiation value.
func PreflightInitialize(
	request *InitializeRequest,
	serverRange *commonv1.ProtocolRange,
	serverBuild *commonv1.BuildIdentity,
	serverCapabilities *commonv1.CapabilitySet,
	serverLimits *commonv1.Limits,
	health *commonv1.Health,
	definitions *DefinitionSet,
) (*InitializeNegotiation, *InitializeResponse) {
	invalid := func(message string) (*InitializeNegotiation, *InitializeResponse) {
		return nil, initializeFailure(commonv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, message)
	}
	internal := func(message string) (*InitializeNegotiation, *InitializeResponse) {
		return nil, initializeFailure(commonv1.ErrorCode_ERROR_CODE_INTERNAL, message)
	}
	if err := ValidateInitializeRequest(request); err != nil {
		return invalid(err.Error())
	}
	if err := validateInitializeServer(
		serverRange, serverBuild, serverCapabilities, serverLimits, health, definitions,
	); err != nil {
		return internal(err.Error())
	}

	version, status := commonv1.NegotiateProtocol(request.GetProtocol(), serverRange)
	if status.GetCode() != commonv1.ErrorCode_ERROR_CODE_OK {
		return nil, initializeStatusFailure(status)
	}
	if err := validateNegotiatedProtocol(version); err != nil {
		return internal("server negotiated an unsupported protocol")
	}
	if err := validateInteractionStreamRequestedLimits(
		version, request.GetRequestedLimits(), serverLimits,
	); err != nil {
		return invalid(err.Error())
	}
	limits, status := commonv1.NegotiateLimits(request.GetRequestedLimits(), serverLimits)
	if status.GetCode() != commonv1.ErrorCode_ERROR_CODE_OK {
		return nil, initializeStatusFailure(status)
	}
	if err := commonv1.ValidateEncodedSize(request, limits.GetMaxMessageBytes()); err != nil {
		return invalid(err.Error())
	}
	capabilities, status := commonv1.NegotiateCapabilities(
		request.GetSupportedCapabilities(),
		request.GetRequiredCapabilities(),
		snapshotCapabilitiesForProtocol(version, serverCapabilities),
	)
	if status.GetCode() != commonv1.ErrorCode_ERROR_CODE_OK {
		return nil, initializeStatusFailure(status)
	}
	if uint64(len(capabilities.GetNames())) > uint64(limits.GetMaxCollectionItems()) {
		return invalid("negotiated capability count exceeds negotiated collection limit")
	}
	if uint64(len(health.GetDegradedReasons())) > uint64(limits.GetMaxCollectionItems()) {
		return invalid("health degraded reason count exceeds negotiated collection limit")
	}
	if err := ValidateDefinitionSet(definitions, limits); err != nil {
		return invalid("server definition set exceeds negotiated limits")
	}

	var reconnectClaim *ReconnectClaim
	if request.GetReconnectClaim() != nil {
		reconnectClaim = clone(request.GetReconnectClaim())
	}
	negotiation := &InitializeNegotiation{
		reconnectClaim: reconnectClaim,
		attemptID:      slices.Clone(request.GetInitializationAttemptId()),
		protocol:       clone(version),
		server:         clone(serverBuild),
		capabilities:   clone(capabilities),
		limits:         clone(limits),
		health:         clone(health),
		definitions:    clone(definitions),
	}
	preview := initializeSuccessResponse(negotiation, strings.Repeat("c", 128), math.MaxUint64)
	if err := commonv1.ValidateEncodedSize(preview, limits.GetMaxMessageBytes()); err != nil {
		return invalid("initialize success response exceeds negotiated message limit")
	}
	if err := ValidateInitializeResponse(preview); err != nil {
		return internal("initialize success response is invalid")
	}
	return negotiation, nil
}

func validateInitializeServer(
	serverRange *commonv1.ProtocolRange,
	serverBuild *commonv1.BuildIdentity,
	serverCapabilities *commonv1.CapabilitySet,
	serverLimits *commonv1.Limits,
	health *commonv1.Health,
	definitions *DefinitionSet,
) error {
	if err := commonv1.ValidateProtocolRange(serverRange); err != nil {
		return errors.New("server protocol range is invalid")
	}
	if err := commonv1.ValidateBuildIdentity(serverBuild); err != nil {
		return errors.New("server build identity is invalid")
	}
	if err := commonv1.ValidateCapabilities(serverCapabilities); err != nil {
		return errors.New("server capabilities are invalid")
	}
	if err := commonv1.ValidateLimits(serverLimits); err != nil {
		return errors.New("server limits are invalid")
	}
	if err := commonv1.ValidateHealth(health); err != nil {
		return errors.New("server health is invalid")
	}
	if !proto.Equal(health.GetLimits(), serverLimits) {
		return errors.New("server health limits do not match server limits")
	}
	if err := ValidateDefinitionSet(definitions, serverLimits); err != nil {
		return errors.New("server definition set is invalid")
	}
	return nil
}

// CompleteInitialize binds a successful pure negotiation to one newly
// allocated or atomically reconnected client ownership result. The host owns
// the remaining allocator invariant: client IDs are valid tokens of at most
// 128 bytes and ownership epochs are positive and reconnect-consistent.
func CompleteInitialize(
	negotiation *InitializeNegotiation,
	clientID string,
	ownershipEpoch uint64,
) *InitializeResponse {
	if negotiation == nil || negotiation.protocol == nil || negotiation.server == nil ||
		negotiation.capabilities == nil || negotiation.limits == nil || negotiation.health == nil ||
		negotiation.definitions == nil {
		return initializeFailure(commonv1.ErrorCode_ERROR_CODE_INTERNAL, "initialize negotiation is invalid")
	}
	if err := validateReconnectResult(negotiation.reconnectClaim, clientID, ownershipEpoch); err != nil {
		return initializeFailure(commonv1.ErrorCode_ERROR_CODE_INTERNAL, "server client ownership is invalid")
	}
	response := initializeSuccessResponse(negotiation, clientID, ownershipEpoch)
	if err := commonv1.ValidateEncodedSize(response, negotiation.limits.GetMaxMessageBytes()); err != nil {
		return initializeFailure(commonv1.ErrorCode_ERROR_CODE_INTERNAL, "initialize response exceeds negotiated message limit")
	}
	return response
}

func initializeSuccessResponse(
	negotiation *InitializeNegotiation,
	clientID string,
	ownershipEpoch uint64,
) *InitializeResponse {
	return &InitializeResponse{
		Status:                  commonv1.OKStatus(),
		Protocol:                clone(negotiation.protocol),
		Server:                  clone(negotiation.server),
		Capabilities:            clone(negotiation.capabilities),
		Limits:                  clone(negotiation.limits),
		Health:                  clone(negotiation.health),
		ClientId:                clientID,
		OwnershipEpoch:          ownershipEpoch,
		Definitions:             clone(negotiation.definitions),
		InitializationAttemptId: slices.Clone(negotiation.attemptID),
	}
}

func validateInitializeRequestAttempt(protocol *commonv1.ProtocolRange, attemptID []byte) error {
	minimum, maximum := protocol.GetMinimum(), protocol.GetMaximum()
	if minimum.GetMajor() == commonv1.ProtocolMajor && maximum.GetMajor() == commonv1.ProtocolMajor &&
		minimum.GetMinor() < InitializationAttemptMinimumMinor &&
		maximum.GetMinor() >= InitializationAttemptMinimumMinor {
		return errors.New("initialize protocol range cannot cross the minor-3 attempt-replay boundary")
	}
	attemptReplayRange := minimum.GetMajor() == commonv1.ProtocolMajor &&
		minimum.GetMinor() >= InitializationAttemptMinimumMinor
	if attemptReplayRange {
		return ValidateInitializationAttemptID(attemptID)
	}
	if len(attemptID) != 0 {
		return errors.New("initialization attempt identity requires a protocol range beginning at minor 3 or newer")
	}
	return nil
}

func validateInitializeResponseAttempt(protocol *commonv1.ProtocolVersion, attemptID []byte) error {
	if protocol.GetMajor() == commonv1.ProtocolMajor &&
		protocol.GetMinor() >= InitializationAttemptMinimumMinor {
		return ValidateInitializationAttemptID(attemptID)
	}
	if len(attemptID) != 0 {
		return errors.New("legacy initialize response must omit the attempt identity")
	}
	return nil
}

// ValidateInitializationAttemptID requires one nonzero canonical 128-bit
// identity. The zero value is reserved as the absent/invalid sentinel.
func ValidateInitializationAttemptID(attemptID []byte) error {
	if len(attemptID) != InitializationAttemptIDBytes {
		return fmt.Errorf("initialization attempt identity must contain exactly %d bytes", InitializationAttemptIDBytes)
	}
	for _, value := range attemptID {
		if value != 0 {
			return nil
		}
	}
	return errors.New("initialization attempt identity must be nonzero")
}

func initializeFailure(code commonv1.ErrorCode, message string) *InitializeResponse {
	return initializeStatusFailure(protocolStatus(code, message))
}

func initializeStatusFailure(status *commonv1.Status) *InitializeResponse {
	response := &InitializeResponse{Status: clone(status)}
	if commonv1.ValidateEncodedSize(response, InitializeBootstrapMaximumBytes) == nil {
		return response
	}
	return &InitializeResponse{Status: protocolStatus(commonv1.ErrorCode_ERROR_CODE_INTERNAL, "initialize negotiation failed")}
}

func validateInitializeProtocolSelection(request *InitializeRequest, response *InitializeResponse) error {
	selected := &commonv1.ProtocolRange{Minimum: response.GetProtocol(), Maximum: response.GetProtocol()}
	negotiated, status := commonv1.NegotiateProtocol(selected, request.GetProtocol())
	if status.GetCode() != commonv1.ErrorCode_ERROR_CODE_OK || !proto.Equal(negotiated, response.GetProtocol()) {
		return errors.New("initialize protocol is outside the requested range")
	}
	return nil
}

func validateInitializeCapabilitySelection(request *InitializeRequest, response *InitializeResponse) error {
	for _, selected := range response.GetCapabilities().GetNames() {
		if !containsCapability(request.GetSupportedCapabilities(), selected) {
			return errors.New("initialize response selected an unsupported capability")
		}
	}
	for _, required := range request.GetRequiredCapabilities().GetNames() {
		if !containsCapability(response.GetCapabilities(), required) {
			return errors.New("initialize response omitted a required capability")
		}
	}
	return nil
}

func validateInitializeLimitSelection(request *InitializeRequest, response *InitializeResponse) error {
	requested, selected, capacity := request.GetRequestedLimits(), response.GetLimits(), response.GetHealth().GetLimits()
	if selected.GetMaxMessageBytes() > requested.GetMaxMessageBytes() ||
		selected.GetMaxCollectionItems() > requested.GetMaxCollectionItems() ||
		selected.GetMaxReplayEvents() > requested.GetMaxReplayEvents() ||
		selected.GetMaxReplayBytes() > requested.GetMaxReplayBytes() ||
		selected.GetMaxConcurrentStreams() > requested.GetMaxConcurrentStreams() ||
		selected.GetMaxActiveRuns() > requested.GetMaxActiveRuns() {
		return errors.New("initialize response exceeds requested limits")
	}
	if err := commonv1.ValidateEncodedSize(request, selected.GetMaxMessageBytes()); err != nil {
		return fmt.Errorf("initialize request exceeds selected message limit: %w", err)
	}
	if selected.GetMaxMessageBytes() > capacity.GetMaxMessageBytes() ||
		selected.GetMaxCollectionItems() > capacity.GetMaxCollectionItems() ||
		selected.GetMaxReplayEvents() > capacity.GetMaxReplayEvents() ||
		selected.GetMaxReplayBytes() > capacity.GetMaxReplayBytes() ||
		selected.GetMaxConcurrentStreams() > capacity.GetMaxConcurrentStreams() ||
		selected.GetMaxActiveRuns() > capacity.GetMaxActiveRuns() {
		return errors.New("initialize response exceeds advertised server capacity")
	}
	return nil
}
