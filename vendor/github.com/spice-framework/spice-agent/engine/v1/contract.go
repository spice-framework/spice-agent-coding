package enginev1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

const (
	// InitializeBootstrapMaximumBytes is the fixed pre-negotiation receive and
	// error-response bound used before connection limits exist.
	InitializeBootstrapMaximumBytes = 1 << 20
	// MaximumSnapshotBytes matches the bounded kernel snapshot contract.
	MaximumSnapshotBytes = 16 << 20
	// MaximumSnapshotEnvelopeOverheadBytes is the exact deterministic Protobuf
	// overhead for the current maximum-width engine/v1 signed envelope shape.
	MaximumSnapshotEnvelopeOverheadBytes = 295
	// MaximumSnapshotEnvelopeBytes bounds a complete signed snapshot transfer.
	MaximumSnapshotEnvelopeBytes = MaximumSnapshotBytes + MaximumSnapshotEnvelopeOverheadBytes
	// InteractionStreamMinimumMinor is the first protocol minor with the
	// complete-snapshot-first interaction stream and no historical replay field.
	InteractionStreamMinimumMinor = uint32(2)
	// InitializationAttemptMinimumMinor is the first protocol minor that makes
	// initialization loss-safe through exact committed-response replay.
	InitializationAttemptMinimumMinor = uint32(3)
	// InitializationAttemptIDBytes is the canonical 128-bit wire identity size.
	InitializationAttemptIDBytes = 16

	maximumJSONBytes        = 1 << 20
	maximumInteractionBytes = 512 << 10
	maximumTokenBytes       = 256
	maximumMessageParts     = 128
	maximumDefinitionTurns  = 1000
)

// ValidateInteractionStreamProtocol rejects negotiated peers that still use
// the provisional pre-minor-2 interaction request semantics.
func ValidateInteractionStreamProtocol(version *commonv1.ProtocolVersion) error {
	if err := validateNegotiatedProtocol(version); err != nil {
		return err
	}
	if version.GetMinor() < InteractionStreamMinimumMinor {
		return errors.New("protocol minor 2 is required for interaction streams")
	}
	return nil
}

func validateInteractionStreamRequestedLimits(
	version *commonv1.ProtocolVersion,
	requested *commonv1.Limits,
	server *commonv1.Limits,
) error {
	if version.GetMinor() < InteractionStreamMinimumMinor {
		return nil
	}
	if requested.GetMaxMessageBytes() < server.GetMaxMessageBytes() ||
		requested.GetMaxCollectionItems() < server.GetMaxCollectionItems() {
		return errors.New("protocol minor 2 requires server-sized interaction snapshot limits")
	}
	return nil
}

func validateInteractionStreamSelectedLimits(
	version *commonv1.ProtocolVersion,
	selected *commonv1.Limits,
	server *commonv1.Limits,
) error {
	if version.GetMinor() < InteractionStreamMinimumMinor {
		return nil
	}
	if selected.GetMaxMessageBytes() != server.GetMaxMessageBytes() ||
		selected.GetMaxCollectionItems() != server.GetMaxCollectionItems() {
		return errors.New("protocol minor 2 requires server-sized interaction snapshot limits")
	}
	return nil
}

// NegotiateInitialize validates the first application payload after transport
// metadata authentication and returns a complete immutable connection contract.
func NegotiateInitialize(
	request *InitializeRequest,
	serverRange *commonv1.ProtocolRange,
	serverBuild *commonv1.BuildIdentity,
	serverCapabilities *commonv1.CapabilitySet,
	serverLimits *commonv1.Limits,
	health *commonv1.Health,
	definitions *DefinitionSet,
	clientID string,
	ownershipEpoch uint64,
) *InitializeResponse {
	negotiation, failure := PreflightInitialize(
		request,
		serverRange,
		serverBuild,
		serverCapabilities,
		serverLimits,
		health,
		definitions,
	)
	if failure != nil {
		return failure
	}
	return CompleteInitialize(negotiation, clientID, ownershipEpoch)
}

// ValidateInitializeResponse lets a client fail closed on malformed negotiation.
func ValidateInitializeResponse(response *InitializeResponse) error {
	if response == nil {
		return errors.New("initialize response is required")
	}
	if err := commonv1.ValidateStatus(response.GetStatus()); err != nil {
		return err
	}
	if statusErr := commonv1.AsError(response.GetStatus()); statusErr != nil {
		if initializeResponseHasNegotiatedFields(response) {
			return errors.New("failed initialize response contains negotiated fields")
		}
		if err := commonv1.ValidateEncodedSize(response, InitializeBootstrapMaximumBytes); err != nil {
			return err
		}
		return statusErr
	}
	if err := validateNegotiatedProtocol(response.GetProtocol()); err != nil {
		return err
	}
	if err := validateInitializeResponseAttempt(response.GetProtocol(), response.GetInitializationAttemptId()); err != nil {
		return err
	}
	if err := commonv1.ValidateBuildIdentity(response.GetServer()); err != nil {
		return err
	}
	if err := commonv1.ValidateCapabilities(response.GetCapabilities()); err != nil {
		return err
	}
	if err := validateInitializeSnapshotCapabilities(
		response.GetProtocol(), response.GetCapabilities(),
	); err != nil {
		return err
	}
	if err := commonv1.ValidateLimits(response.GetLimits()); err != nil {
		return err
	}
	if uint64(len(response.GetCapabilities().GetNames())) > uint64(response.GetLimits().GetMaxCollectionItems()) {
		return errors.New("initialize capability count exceeds the negotiated collection limit")
	}
	if err := commonv1.ValidateHealth(response.GetHealth()); err != nil {
		return err
	}
	if err := validateInteractionStreamSelectedLimits(
		response.GetProtocol(), response.GetLimits(), response.GetHealth().GetLimits(),
	); err != nil {
		return err
	}
	if uint64(len(response.GetHealth().GetDegradedReasons())) > uint64(response.GetLimits().GetMaxCollectionItems()) {
		return errors.New("initialize health reason count exceeds the negotiated collection limit")
	}
	if err := token("client ID", response.GetClientId(), 128); err != nil {
		return err
	}
	if response.GetOwnershipEpoch() == 0 {
		return errors.New("ownership epoch must be positive")
	}
	if err := ValidateDefinitionSet(response.GetDefinitions(), response.GetLimits()); err != nil {
		return err
	}
	return commonv1.ValidateEncodedSize(response, response.GetLimits().GetMaxMessageBytes())
}

func validateInitializeSnapshotCapabilities(
	protocol *commonv1.ProtocolVersion,
	capabilities *commonv1.CapabilitySet,
) error {
	if !protocolSupportsSnapshotAuthority(protocol) &&
		(containsCapability(capabilities, CapabilitySnapshotAuthorityV1) ||
			containsCapability(capabilities, "snapshots")) {
		return errors.New("protocol minor 1 is required for snapshot transfer")
	}
	return nil
}

func initializeResponseHasNegotiatedFields(response *InitializeResponse) bool {
	return response.GetProtocol() != nil || response.GetServer() != nil || response.GetCapabilities() != nil ||
		response.GetLimits() != nil || response.GetHealth() != nil || response.GetClientId() != "" ||
		response.GetOwnershipEpoch() != 0 || response.GetDefinitions() != nil ||
		len(response.GetInitializationAttemptId()) != 0
}

// ValidateInitializeResponseForRequest additionally verifies that ownership is
// the exact result of the request's new-owner or reconnect CAS contract.
func ValidateInitializeResponseForRequest(request *InitializeRequest, response *InitializeResponse) error {
	if err := ValidateInitializeRequest(request); err != nil {
		return err
	}
	if err := ValidateInitializeResponse(response); err != nil {
		return err
	}
	if err := validateInitializeProtocolSelection(request, response); err != nil {
		return err
	}
	if err := validateInitializeCapabilitySelection(request, response); err != nil {
		return err
	}
	if err := validateInitializeLimitSelection(request, response); err != nil {
		return err
	}
	if !bytes.Equal(request.GetInitializationAttemptId(), response.GetInitializationAttemptId()) {
		return errors.New("initialize response attempt identity does not match the request")
	}
	return validateReconnectResult(request.GetReconnectClaim(), response.GetClientId(), response.GetOwnershipEpoch())
}

// ValidateDefinitionSet rejects mutable-looking, ambiguous, or unsorted server
// catalogs. Generated definitions are authoritative; clients send only refs.
func ValidateDefinitionSet(value *DefinitionSet, limits *commonv1.Limits) error {
	if value == nil {
		return errors.New("definition set is required")
	}
	if err := token("definition set revision", value.GetRevision(), maximumTokenBytes); err != nil {
		return err
	}
	if err := commonv1.ValidateLimits(limits); err != nil {
		return err
	}
	if len(value.GetDefinitions()) == 0 || uint64(len(value.GetDefinitions())) > uint64(limits.GetMaxCollectionItems()) {
		return fmt.Errorf("definition count must be between 1 and %d", limits.GetMaxCollectionItems())
	}
	previous := ""
	for index, definition := range value.GetDefinitions() {
		if err := validateDefinition(definition); err != nil {
			return fmt.Errorf("definition %d: %w", index, err)
		}
		key := definition.GetId() + "\x00" + definition.GetRevision()
		if key <= previous {
			return errors.New("definitions must be sorted and unique by ID and revision")
		}
		previous = key
	}
	return commonv1.ValidateEncodedSize(value, limits.GetMaxMessageBytes())
}

func validateDefinition(value *Definition) error {
	if value == nil {
		return errors.New("definition is required")
	}
	for _, field := range [][2]string{
		{"definition ID", value.GetId()},
		{"definition revision", value.GetRevision()},
		{"model", value.GetModel()},
	} {
		if err := token(field[0], field[1], maximumTokenBytes); err != nil {
			return err
		}
	}
	if value.GetMaxTurns() == 0 || value.GetMaxTurns() > maximumDefinitionTurns {
		return fmt.Errorf("agent definition max turns must be between 1 and %d", maximumDefinitionTurns)
	}
	return nil
}

// ResolveDefinition returns the exact immutable server definition selected by
// a minimal client reference.
func ResolveDefinition(reference *AgentDefinitionRef, definitions *DefinitionSet, limits *commonv1.Limits) (*Definition, error) {
	if reference == nil {
		return nil, errors.New("agent definition reference is required")
	}
	if err := token("definition ID", reference.GetId(), maximumTokenBytes); err != nil {
		return nil, err
	}
	if err := token("definition revision", reference.GetRevision(), maximumTokenBytes); err != nil {
		return nil, err
	}
	if err := ValidateDefinitionSet(definitions, limits); err != nil {
		return nil, err
	}
	for _, definition := range definitions.GetDefinitions() {
		if definition.GetId() == reference.GetId() && definition.GetRevision() == reference.GetRevision() {
			return clone(definition), nil
		}
	}
	return nil, fmt.Errorf("agent definition %q at revision %q is unavailable", reference.GetId(), reference.GetRevision())
}

func validateReconnectClaim(claim *ReconnectClaim) error {
	if claim == nil {
		return nil
	}
	if err := token("reconnect client ID", claim.GetClientId(), 128); err != nil {
		return err
	}
	if claim.GetExpectedOwnershipEpoch() == 0 || claim.GetExpectedOwnershipEpoch() == math.MaxUint64 {
		return errors.New("reconnect expected ownership epoch must be incrementable")
	}
	return nil
}

func validateReconnectResult(claim *ReconnectClaim, clientID string, ownershipEpoch uint64) error {
	if err := token("client ID", clientID, 128); err != nil {
		return err
	}
	if claim == nil {
		if ownershipEpoch != 1 {
			return errors.New("new client ownership epoch must be one")
		}
		return nil
	}
	if err := validateReconnectClaim(claim); err != nil {
		return err
	}
	if clientID != claim.GetClientId() || ownershipEpoch != claim.GetExpectedOwnershipEpoch()+1 {
		return errors.New("reconnect result does not satisfy the ownership epoch compare-and-swap")
	}
	return nil
}

// CheckClientOwnership returns a typed stale-client status on identity or epoch
// mismatch. A nil return means the client still owns the connection.
func CheckClientOwnership(
	clientID string,
	observedEpoch uint64,
	expectedClientID string,
	expectedEpoch uint64,
) *commonv1.Status {
	if clientID == expectedClientID && observedEpoch == expectedEpoch && expectedEpoch != 0 {
		return nil
	}
	return &commonv1.Status{
		Code:    commonv1.ErrorCode_ERROR_CODE_STALE_CLIENT,
		Message: "client ownership is stale",
		Detail: &commonv1.Status_StaleClient{StaleClient: &commonv1.StaleClient{
			ExpectedEpoch: expectedEpoch,
			ObservedEpoch: observedEpoch,
		}},
	}
}

// ValidateStartRunRequest rejects unbounded or provider-specific run input.
func ValidateStartRunRequest(request *StartRunRequest, limits *commonv1.Limits) error {
	if request == nil {
		return errors.New("start run request is required")
	}
	if err := commonv1.ValidateLimits(limits); err != nil {
		return err
	}
	if err := validateClientMutation(
		request.GetClientId(),
		request.GetOwnershipEpoch(),
		request.GetClientOperationId(),
	); err != nil {
		return err
	}
	definition := request.GetDefinition()
	if definition == nil {
		return errors.New("agent definition reference is required")
	}
	for _, field := range [][2]string{
		{"definition ID", definition.GetId()},
		{"definition revision", definition.GetRevision()},
	} {
		if err := token(field[0], field[1], maximumTokenBytes); err != nil {
			return err
		}
	}
	if err := ValidateMessage(request.GetInput()); err != nil {
		return fmt.Errorf("initial message: %w", err)
	}
	if request.GetInput().GetRole() != MessageRole_MESSAGE_ROLE_USER {
		return errors.New("initial message must have the user role")
	}
	if uint64(len(request.GetInput().GetParts())) > uint64(limits.GetMaxCollectionItems()) {
		return fmt.Errorf("initial message part count exceeds %d", limits.GetMaxCollectionItems())
	}
	return commonv1.ValidateEncodedSize(request, limits.GetMaxMessageBytes())
}

// ValidateMessage enforces the provider-neutral bounded message union.
func ValidateMessage(value *Message) error {
	if value == nil {
		return errors.New("message is required")
	}
	if err := token("message ID", value.GetId(), 128); err != nil {
		return err
	}
	switch value.GetRole() {
	case MessageRole_MESSAGE_ROLE_SYSTEM,
		MessageRole_MESSAGE_ROLE_USER,
		MessageRole_MESSAGE_ROLE_ASSISTANT,
		MessageRole_MESSAGE_ROLE_TOOL:
	default:
		return fmt.Errorf("message role %d is unsupported", value.GetRole())
	}
	if len(value.GetParts()) == 0 || len(value.GetParts()) > maximumMessageParts {
		return fmt.Errorf("message part count must be between 1 and %d", maximumMessageParts)
	}
	for index, part := range value.GetParts() {
		if err := validateContentPart(part); err != nil {
			return fmt.Errorf("message part %d: %w", index, err)
		}
	}
	return nil
}

// ValidateStreamEventsRequest enforces replay and connection bounds.
func ValidateStreamEventsRequest(
	request *StreamEventsRequest,
	limits *commonv1.Limits,
) error {
	if request == nil {
		return errors.New("stream events request is required")
	}
	if err := validateClient(request.GetClientId(), request.GetOwnershipEpoch()); err != nil {
		return err
	}
	if err := token("run ID", request.GetRunId(), 128); err != nil {
		return err
	}
	if err := commonv1.ValidateLimits(limits); err != nil {
		return err
	}
	if request.GetReplayLimit() == 0 || request.GetReplayLimit() > limits.GetMaxReplayEvents() {
		return fmt.Errorf("replay limit must be between 1 and %d", limits.GetMaxReplayEvents())
	}
	return commonv1.ValidateEncodedSize(request, limits.GetMaxMessageBytes())
}

// ValidateStreamControl enforces captured bounds and an explicit page/tail
// relationship. The optional page cursor permits additive older-peer handling.
func ValidateStreamControl(value *StreamControl) error {
	if value == nil {
		return errors.New("stream control is required")
	}
	if err := commonv1.ValidateStatus(value.GetStatus()); err != nil {
		return err
	}
	if value.GetStatus().GetCode() != commonv1.ErrorCode_ERROR_CODE_OK {
		return nil
	}
	return validateSuccessfulStreamControl(value)
}

func validateSuccessfulStreamControl(value *StreamControl) error {
	earliest, latest := value.GetEarliestSequence(), value.GetLatestSequence()
	validWindow := earliest > 0 && latest >= earliest
	emptyWindow := earliest > 0 && latest < math.MaxUint64 && earliest == latest+1
	if !validWindow && !emptyWindow {
		return errors.New("stream control retained bounds are invalid")
	}
	if value.PageLastSequence == nil {
		return validateLegacyStreamControl(value, earliest, latest)
	}
	return validateCurrentStreamControl(value, earliest, latest)
}

func validateLegacyStreamControl(value *StreamControl, earliest, latest uint64) error {
	if value.GetHasMore() || value.GetTailing() {
		return errors.New("stream control page cursor is required for paging or tailing")
	}
	// Compatibility-only shape for a provisional older peer. Current
	// successful paging/tailing controls always set page_last_sequence.
	if value.GetLastDeliveredSequence() < earliest-1 || value.GetLastDeliveredSequence() > latest {
		return errors.New("legacy stream control delivery cursor is outside retained bounds")
	}
	return nil
}

func validateCurrentStreamControl(value *StreamControl, earliest, latest uint64) error {
	pageLast := value.GetPageLastSequence()
	if pageLast < earliest-1 || pageLast > latest ||
		value.GetLastDeliveredSequence() < earliest-1 || value.GetLastDeliveredSequence() > pageLast {
		return errors.New("stream control cursors exceed the captured latest sequence")
	}
	if value.GetHasMore() != (pageLast < latest) {
		return errors.New("stream control has_more must exactly identify a non-final page")
	}
	if value.GetHasMore() && value.GetTailing() {
		return errors.New("stream control cannot tail a non-final page")
	}
	if value.GetTailing() && pageLast != latest {
		return errors.New("stream control may tail only from the captured latest sequence")
	}
	return nil
}

// ValidateStreamInteractionsRequest bounds the separate pending-prompt stream.
// Reconnect always receives a complete current snapshot, so there is no
// historical delta replay limit to validate.
func ValidateStreamInteractionsRequest(
	request *StreamInteractionsRequest,
	protocol *commonv1.ProtocolVersion,
	limits *commonv1.Limits,
) error {
	if request == nil {
		return errors.New("stream interactions request is required")
	}
	if err := ValidateInteractionStreamProtocol(protocol); err != nil {
		return err
	}
	if err := validateClient(request.GetClientId(), request.GetOwnershipEpoch()); err != nil {
		return err
	}
	if err := commonv1.ValidateLimits(limits); err != nil {
		return err
	}
	legacyReplay, err := hasUnknownFieldNumber(request.ProtoReflect().GetUnknown(), 3)
	if err != nil {
		return err
	}
	if legacyReplay {
		return errors.New("provisional interaction replay field is unsupported")
	}
	return commonv1.ValidateEncodedSize(request, limits.GetMaxMessageBytes())
}

func hasUnknownFieldNumber(encoded []byte, field protowire.Number) (bool, error) {
	for len(encoded) != 0 {
		number, kind, tagBytes := protowire.ConsumeTag(encoded)
		if tagBytes < 0 {
			return false, errors.New("interaction request contains malformed unknown fields")
		}
		valueBytes := protowire.ConsumeFieldValue(number, kind, encoded[tagBytes:])
		if valueBytes < 0 {
			return false, errors.New("interaction request contains malformed unknown fields")
		}
		if number == field {
			return true, nil
		}
		encoded = encoded[tagBytes+valueBytes:]
	}
	return false, nil
}

// ValidatePendingInteraction rejects uncorrelated or unbounded prompt state.
func ValidatePendingInteraction(value *PendingInteraction) error {
	if value == nil {
		return errors.New("pending interaction is required")
	}
	for _, field := range [][2]string{
		{"run ID", value.GetRunId()},
		{"interaction ID", value.GetInteractionId()},
		{"interaction kind", value.GetKind()},
	} {
		if err := token(field[0], field[1], 128); err != nil {
			return err
		}
	}
	if value.GetPrompt() == "" || value.GetPrompt() != strings.TrimSpace(value.GetPrompt()) || len(value.GetPrompt()) > maximumInteractionBytes {
		return errors.New("interaction prompt must be non-empty, trimmed, and bounded")
	}
	return validateBoundedJSON("interaction schema", value.GetSchemaJson(), maximumInteractionBytes)
}

// ValidateInteractionSnapshot enforces an atomic sorted pending-set snapshot.
func ValidateInteractionSnapshot(value *InteractionSnapshot, limits *commonv1.Limits) error {
	if value == nil {
		return errors.New("interaction snapshot is required")
	}
	if err := commonv1.ValidateLimits(limits); err != nil {
		return err
	}
	if uint64(len(value.GetPending())) > uint64(limits.GetMaxCollectionItems()) {
		return fmt.Errorf("pending interaction count exceeds %d", limits.GetMaxCollectionItems())
	}
	previous := ""
	for index, pending := range value.GetPending() {
		if err := ValidatePendingInteraction(pending); err != nil {
			return fmt.Errorf("pending interaction %d: %w", index, err)
		}
		key := pending.GetRunId() + "\x00" + pending.GetInteractionId()
		if key <= previous {
			return errors.New("pending interactions must be sorted and unique by run and interaction ID")
		}
		previous = key
	}
	return commonv1.ValidateEncodedSize(value, limits.GetMaxMessageBytes())
}

// ValidateInteractionDelta enforces one strictly revisioned opened/closed item.
func ValidateInteractionDelta(value *InteractionDelta) error {
	if value == nil {
		return errors.New("interaction delta is required")
	}
	if value.GetRevision() == 0 {
		return errors.New("interaction delta revision must be positive")
	}
	if value.GetKind() != InteractionDeltaKind_INTERACTION_DELTA_KIND_OPENED &&
		value.GetKind() != InteractionDeltaKind_INTERACTION_DELTA_KIND_CLOSED {
		return fmt.Errorf("interaction delta kind %d is unsupported", value.GetKind())
	}
	return ValidatePendingInteraction(value.GetInteraction())
}

// ValidateInteractionStreamControl enforces paging before live delta tailing.
func ValidateInteractionStreamControl(value *InteractionStreamControl) error {
	if value == nil {
		return errors.New("interaction stream control is required")
	}
	if err := commonv1.ValidateStatus(value.GetStatus()); err != nil {
		return err
	}
	if value.GetStatus().GetCode() != commonv1.ErrorCode_ERROR_CODE_OK {
		return nil
	}
	if value.GetPageLastRevision() > value.GetLatestRevision() {
		return errors.New("interaction page cursor exceeds the captured latest revision")
	}
	if value.GetHasMore() != (value.GetPageLastRevision() < value.GetLatestRevision()) {
		return errors.New("interaction has_more must exactly identify a non-final page")
	}
	if value.GetHasMore() && value.GetTailing() {
		return errors.New("interaction stream cannot tail a non-final page")
	}
	if value.GetTailing() && value.GetPageLastRevision() != value.GetLatestRevision() {
		return errors.New("interaction stream may tail only from the captured latest revision")
	}
	return nil
}

// ValidateInteractionStreamPage proves the mandatory reconnect contract: the
// initial page is exactly one complete atomic pending snapshot followed by its
// captured control. A tail, when requested, begins only after that control and
// carries revision-contiguous live deltas. A client therefore never depends on
// evicted delta history to discover an unresolved prompt.
func ValidateInteractionStreamPage(
	values []*StreamInteractionsResponse,
	expectedTail bool,
	limits *commonv1.Limits,
) error {
	if len(values) != 2 {
		return errors.New("interaction stream initial page requires exactly a snapshot and control")
	}
	if err := commonv1.ValidateLimits(limits); err != nil {
		return err
	}
	for index, value := range values {
		if err := commonv1.ValidateEncodedSize(value, limits.GetMaxMessageBytes()); err != nil {
			return fmt.Errorf("interaction frame %d: %w", index, err)
		}
	}
	snapshot := values[0].GetSnapshot()
	if err := ValidateInteractionSnapshot(snapshot, limits); err != nil {
		return fmt.Errorf("first interaction frame: %w", err)
	}
	control := values[1].GetControl()
	if err := ValidateInteractionStreamControl(control); err != nil {
		return fmt.Errorf("interaction control frame: %w", err)
	}
	if commonv1.AsError(control.GetStatus()) != nil {
		return errors.New("interaction initial page control must be successful")
	}
	if control.GetLatestRevision() != snapshot.GetRevision() ||
		control.GetPageLastRevision() != snapshot.GetRevision() || control.GetHasMore() {
		return errors.New("interaction control must identify the complete snapshot revision")
	}
	if control.GetTailing() != expectedTail {
		return errors.New("interaction control tail state does not match the request")
	}
	return nil
}

// InteractionTailValidator validates the stateful live-delta contract after a
// complete snapshot and its control have been accepted. It is single-consumer
// state: failed deltas never advance the revision or pending-set membership.
type InteractionTailValidator struct {
	revision uint64
	limits   *commonv1.Limits
	pending  map[string]*PendingInteraction
}

// NewInteractionTailValidator validates a tailing initial page and captures a
// defensive pending-set baseline for subsequent Accept calls.
func NewInteractionTailValidator(
	snapshot *InteractionSnapshot,
	control *InteractionStreamControl,
	limits *commonv1.Limits,
) (*InteractionTailValidator, error) {
	page := []*StreamInteractionsResponse{
		{Payload: &StreamInteractionsResponse_Snapshot{Snapshot: snapshot}},
		{Payload: &StreamInteractionsResponse_Control{Control: control}},
	}
	if err := ValidateInteractionStreamPage(page, true, limits); err != nil {
		return nil, err
	}
	validator := &InteractionTailValidator{
		revision: snapshot.GetRevision(), limits: proto.CloneOf(limits),
		pending: make(map[string]*PendingInteraction, len(snapshot.GetPending())),
	}
	for _, item := range snapshot.GetPending() {
		validator.pending[interactionKey(item)] = proto.CloneOf(item)
	}
	return validator, nil
}

// Accept validates and commits exactly one complete next live-delta frame.
func (validator *InteractionTailValidator) Accept(frame *StreamInteractionsResponse) error {
	if validator == nil || validator.pending == nil || validator.limits == nil {
		return errors.New("interaction tail validator is unavailable")
	}
	if err := commonv1.ValidateEncodedSize(frame, validator.limits.GetMaxMessageBytes()); err != nil {
		return err
	}
	delta := frame.GetDelta()
	if err := ValidateInteractionDelta(delta); err != nil {
		return err
	}
	if validator.revision == math.MaxUint64 || delta.GetRevision() != validator.revision+1 {
		return errors.New("interaction delta is not the next revision")
	}
	key := interactionKey(delta.GetInteraction())
	existing, exists := validator.pending[key]
	next := make(map[string]*PendingInteraction, len(validator.pending)+1)
	maps.Copy(next, validator.pending)
	switch delta.GetKind() {
	case InteractionDeltaKind_INTERACTION_DELTA_KIND_OPENED:
		if exists {
			return errors.New("interaction delta opens an already-pending interaction")
		}
		if uint64(len(validator.pending)) >= uint64(validator.limits.GetMaxCollectionItems()) {
			return errors.New("interaction delta exceeds the negotiated pending count")
		}
		next[key] = proto.CloneOf(delta.GetInteraction())
	case InteractionDeltaKind_INTERACTION_DELTA_KIND_CLOSED:
		if !exists {
			return errors.New("interaction delta closes a non-pending interaction")
		}
		if !proto.Equal(existing, delta.GetInteraction()) {
			return errors.New("interaction close does not match the pending interaction")
		}
		delete(next, key)
	default:
		return errors.New("interaction delta kind is unsupported")
	}
	if err := validateRepresentableInteractionState(delta.GetRevision(), next, validator.limits); err != nil {
		return err
	}
	validator.pending = next
	validator.revision = delta.GetRevision()
	return nil
}

func validateRepresentableInteractionState(
	revision uint64,
	pending map[string]*PendingInteraction,
	limits *commonv1.Limits,
) error {
	keys := make([]string, 0, len(pending))
	for key := range pending {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	items := make([]*PendingInteraction, len(keys))
	for index, key := range keys {
		items[index] = pending[key]
	}
	snapshot := &InteractionSnapshot{Revision: revision, Pending: items}
	if err := ValidateInteractionSnapshot(snapshot, limits); err != nil {
		return fmt.Errorf("interaction state is not reconnectable: %w", err)
	}
	frame := &StreamInteractionsResponse{
		Payload: &StreamInteractionsResponse_Snapshot{Snapshot: snapshot},
	}
	if err := commonv1.ValidateEncodedSize(frame, limits.GetMaxMessageBytes()); err != nil {
		return fmt.Errorf("interaction state is not reconnectable: %w", err)
	}
	return nil
}

// Revision returns the last accepted snapshot or delta revision.
func (validator *InteractionTailValidator) Revision() uint64 {
	if validator == nil {
		return 0
	}
	return validator.revision
}

func interactionKey(value *PendingInteraction) string {
	return value.GetRunId() + "\x00" + value.GetInteractionId()
}

// CheckReplayCursor returns typed recovery bounds when retained events cannot
// satisfy the requested cursor.
func CheckReplayCursor(
	requestedAfter,
	earliest,
	latest uint64,
) *commonv1.Status {
	validWindow := earliest > 0 && latest >= earliest
	emptyWindow := earliest > 0 && latest < math.MaxUint64 && earliest == latest+1
	if emptyWindow && requestedAfter == latest || validWindow && requestedAfter != math.MaxUint64 &&
		requestedAfter+1 >= earliest && requestedAfter <= latest {
		return nil
	}
	recovery := latest
	if requestedAfter < earliest {
		recovery = earliest - 1
	}
	return &commonv1.Status{
		Code:    commonv1.ErrorCode_ERROR_CODE_OUT_OF_RANGE,
		Message: "requested event cursor is outside the retained replay window",
		Detail: &commonv1.Status_ReplayBounds{ReplayBounds: &commonv1.ReplayBounds{
			RequestedAfterSequence: requestedAfter,
			EarliestSequence:       earliest,
			LatestSequence:         latest,
			RecoverySequence:       recovery,
		}},
	}
}

// ValidateEventBatch enforces a contiguous ordered replay after one cursor.
func ValidateEventBatch(
	runID string,
	afterSequence uint64,
	events []*RunEvent,
	limits *commonv1.Limits,
) error {
	if err := token("run ID", runID, 128); err != nil {
		return err
	}
	if err := commonv1.ValidateLimits(limits); err != nil {
		return err
	}
	if uint64(len(events)) > uint64(limits.GetMaxReplayEvents()) {
		return fmt.Errorf("event count %d exceeds %d", len(events), limits.GetMaxReplayEvents())
	}
	expected := afterSequence
	var total uint64
	for index, current := range events {
		if expected == math.MaxUint64 {
			return errors.New("event sequence overflow")
		}
		expected++
		if err := ValidateRunEvent(current); err != nil {
			return fmt.Errorf("event %d: %w", index, err)
		}
		if current.GetRunId() != runID || current.GetSequence() != expected {
			return fmt.Errorf("event %d is not the next ordered event", index)
		}
		size := proto.Size(current)
		if size < 0 {
			return fmt.Errorf("event %d has an invalid encoded size", index)
		}
		// #nosec G115 -- the explicit non-negative guard makes every int value safe in uint64.
		total += uint64(size)
		if total > limits.GetMaxReplayBytes() {
			return fmt.Errorf("event replay size %d exceeds %d", total, limits.GetMaxReplayBytes())
		}
	}
	return nil
}

// ValidateRunEvent rejects unknown lifecycle kinds and malformed envelopes.
func ValidateRunEvent(value *RunEvent) error {
	if value == nil {
		return errors.New("run event is required")
	}
	if err := token("run ID", value.GetRunId(), 128); err != nil {
		return err
	}
	if value.GetSequence() == 0 || value.GetUnixNano() <= 0 {
		return errors.New("run event requires positive sequence and timestamp")
	}
	if !knownEventKind(value.GetKind()) {
		return fmt.Errorf("run event kind %d is unsupported", value.GetKind())
	}
	if value.GetTerminal() != terminalEventKind(value.GetKind()) {
		return errors.New("run event terminal flag does not match its kind")
	}
	if len(value.GetPayloadJson()) > maximumJSONBytes ||
		(len(value.GetPayloadJson()) != 0 && !json.Valid(value.GetPayloadJson())) {
		return errors.New("run event payload must be empty or bounded valid JSON")
	}
	return nil
}

// OverloadStatus creates a typed non-retryable overload result.
func OverloadStatus(resource string, limit, observed uint64) *commonv1.Status {
	return &commonv1.Status{
		Code:    commonv1.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED,
		Message: "protocol resource limit exceeded",
		Detail: &commonv1.Status_Overload{Overload: &commonv1.Overload{
			Resource: resource,
			Limit:    limit,
			Observed: observed,
		}},
	}
}

// ValidateCancelRunRequest validates a bounded idempotent cancellation mutation.
func ValidateCancelRunRequest(request *CancelRunRequest, limits *commonv1.Limits) error {
	if request == nil {
		return errors.New("cancel run request is required")
	}
	if err := validateClientMutation(
		request.GetClientId(),
		request.GetOwnershipEpoch(),
		request.GetClientOperationId(),
	); err != nil {
		return err
	}
	if err := token("run ID", request.GetRunId(), 128); err != nil {
		return err
	}
	if request.GetReason() != "" {
		if err := token("cancellation reason", request.GetReason(), 1024); err != nil {
			return err
		}
	}
	return validateUnarySize(request, limits)
}

// ValidateSuspendRunRequest validates one bounded idempotent safe-boundary mutation.
func ValidateSuspendRunRequest(request *SuspendRunRequest, limits *commonv1.Limits) error {
	if request == nil {
		return errors.New("suspend run request is required")
	}
	if err := validateRunMutation(request.GetClientId(), request.GetOwnershipEpoch(), request.GetClientOperationId(), request.GetRunId()); err != nil {
		return err
	}
	return validateUnarySize(request, limits)
}

// ValidateResumeRunRequest validates one bounded idempotent local-resume mutation.
func ValidateResumeRunRequest(request *ResumeRunRequest, limits *commonv1.Limits) error {
	if request == nil {
		return errors.New("resume run request is required")
	}
	if err := validateRunMutation(request.GetClientId(), request.GetOwnershipEpoch(), request.GetClientOperationId(), request.GetRunId()); err != nil {
		return err
	}
	return validateUnarySize(request, limits)
}

// ValidateInteractionResponse rejects stale ownership, wrong correlation, and
// malformed structured input before it can reach the kernel broker.
func ValidateInteractionResponse(
	request *RespondInteractionRequest,
	expectedClientID string,
	expectedEpoch uint64,
	expectedRunID string,
	expectedInteractionID string,
) *commonv1.Status {
	if request == nil {
		return protocolStatus(commonv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "interaction response is required")
	}
	if stale := CheckClientOwnership(
		request.GetClientId(),
		request.GetOwnershipEpoch(),
		expectedClientID,
		expectedEpoch,
	); stale != nil {
		return stale
	}
	if err := validateRespondInteractionFields(request); err != nil {
		return protocolStatus(commonv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err.Error())
	}
	if request.GetRunId() != expectedRunID || request.GetInteractionId() != expectedInteractionID {
		return protocolStatus(commonv1.ErrorCode_ERROR_CODE_CONFLICT, "interaction correlation does not match the pending request")
	}
	return commonv1.OKStatus()
}

func validateClientMutation(clientID string, epoch uint64, operationID string) error {
	if err := validateClient(clientID, epoch); err != nil {
		return err
	}
	return token("client operation ID", operationID, 128)
}

func validateRunMutation(clientID string, epoch uint64, operationID, runID string) error {
	if err := validateClientMutation(clientID, epoch, operationID); err != nil {
		return err
	}
	return token("run ID", runID, 128)
}

func validateClient(clientID string, epoch uint64) error {
	if err := token("client ID", clientID, 128); err != nil {
		return err
	}
	if epoch == 0 {
		return errors.New("ownership epoch must be positive")
	}
	return nil
}

func validateContentPart(part *ContentPart) error {
	if part == nil {
		return errors.New("content part is required")
	}
	switch value := part.GetValue().(type) {
	case *ContentPart_Text:
		return validateTextPart(value)
	case *ContentPart_ToolCall:
		return validateToolCallPart(value)
	case *ContentPart_ToolResult:
		return validateToolResultPart(value)
	case *ContentPart_Extension:
		return validateExtensionPart(value)
	default:
		return errors.New("content part union is unspecified")
	}
}

func validateTextPart(value *ContentPart_Text) error {
	if value == nil {
		return errors.New("text part is required")
	}
	if value.Text == "" || len(value.Text) > maximumJSONBytes {
		return errors.New("text part must be non-empty and bounded")
	}
	return nil
}

func validateToolCallPart(value *ContentPart_ToolCall) error {
	if value == nil || value.ToolCall == nil {
		return errors.New("tool call part is required")
	}
	if err := token("tool call ID", value.ToolCall.GetCallId(), 128); err != nil {
		return err
	}
	if err := token("tool name", value.ToolCall.GetName(), 128); err != nil {
		return err
	}
	return validateJSON("tool arguments", value.ToolCall.GetArgumentsJson())
}

func validateToolResultPart(value *ContentPart_ToolResult) error {
	if value == nil || value.ToolResult == nil {
		return errors.New("tool result part is required")
	}
	if err := token("tool call ID", value.ToolResult.GetCallId(), 128); err != nil {
		return err
	}
	if err := token("tool name", value.ToolResult.GetName(), 128); err != nil {
		return err
	}
	return validateJSON("tool result", value.ToolResult.GetResultJson())
}

func validateExtensionPart(value *ContentPart_Extension) error {
	if value == nil || value.Extension == nil {
		return errors.New("extension part is required")
	}
	if err := token("extension namespace", value.Extension.GetNamespace(), maximumTokenBytes); err != nil {
		return err
	}
	return validateJSON("extension value", value.Extension.GetValueJson())
}

func validateJSON(label string, value []byte) error {
	return validateBoundedJSON(label, value, maximumJSONBytes)
}

func validateBoundedJSON(label string, value []byte, maximum int) error {
	if len(value) == 0 || len(value) > maximum || !json.Valid(value) {
		return fmt.Errorf("%s must be bounded valid JSON", label)
	}
	return nil
}

func knownEventKind(kind EventKind) bool {
	return kind >= EventKind_EVENT_KIND_RUN_STARTED &&
		kind <= EventKind_EVENT_KIND_INTERACTION_CANCELLED
}

func terminalEventKind(kind EventKind) bool {
	switch kind {
	case EventKind_EVENT_KIND_RUN_COMPLETED,
		EventKind_EVENT_KIND_RUN_FAILED,
		EventKind_EVENT_KIND_RUN_CANCELLED,
		EventKind_EVENT_KIND_TURN_COMPLETED,
		EventKind_EVENT_KIND_TURN_FAILED,
		EventKind_EVENT_KIND_MODEL_COMPLETED,
		EventKind_EVENT_KIND_MODEL_FAILED,
		EventKind_EVENT_KIND_TOOL_COMPLETED,
		EventKind_EVENT_KIND_TOOL_FAILED,
		EventKind_EVENT_KIND_INTERACTION_COMPLETED,
		EventKind_EVENT_KIND_INTERACTION_FAILED,
		EventKind_EVENT_KIND_INTERACTION_CANCELLED:
		return true
	default:
		return false
	}
}

func token(label, value string, maximum int) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be non-empty without surrounding whitespace", label)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", label)
	}
	if strings.ContainsAny(value, "\x00\r\n\t") {
		return fmt.Errorf("%s must not contain control characters", label)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	return nil
}

func protocolStatus(code commonv1.ErrorCode, message string) *commonv1.Status {
	return &commonv1.Status{Code: code, Message: message}
}

func clone[T proto.Message](value T) T {
	result, ok := proto.Clone(value).(T)
	if !ok {
		panic("protobuf clone changed concrete message type")
	}
	return result
}
