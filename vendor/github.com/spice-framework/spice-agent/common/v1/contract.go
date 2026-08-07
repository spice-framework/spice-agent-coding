package commonv1

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
)

const (
	// ProtocolMajor is the architecture-proof wire major.
	ProtocolMajor = uint32(1)
	// ProtocolMinimumMinor is the oldest compatible architecture-proof wire minor.
	ProtocolMinimumMinor = uint32(0)
	// ProtocolMinor is the highest architecture-proof wire minor. Minor 3 adds
	// caller-identified initialization attempts with exact success replay.
	ProtocolMinor = uint32(3)
	// ProtocolPatch is the architecture-proof wire patch.
	ProtocolPatch      = uint32(0)
	maximumTokenBytes  = 256
	maximumStatusBytes = 2048
)

// SupportedProtocolRange returns a fresh immutable-by-convention range for the
// versions implemented by this package.
func SupportedProtocolRange() *ProtocolRange {
	return &ProtocolRange{
		Minimum: &ProtocolVersion{Major: ProtocolMajor, Minor: ProtocolMinimumMinor, Patch: ProtocolPatch},
		Maximum: &ProtocolVersion{Major: ProtocolMajor, Minor: ProtocolMinor, Patch: ProtocolPatch},
	}
}

// OKStatus returns a fresh successful application status.
func OKStatus() *Status { return &Status{Code: ErrorCode_ERROR_CODE_OK} }

// ValidateProtocolRange rejects nil, inverted, or unspecified version ranges.
func ValidateProtocolRange(value *ProtocolRange) error {
	if value == nil || value.GetMinimum() == nil || value.GetMaximum() == nil {
		return errors.New("protocol range requires minimum and maximum versions")
	}
	if err := validateProtocolVersion(value.GetMinimum()); err != nil {
		return fmt.Errorf("minimum protocol version: %w", err)
	}
	if err := validateProtocolVersion(value.GetMaximum()); err != nil {
		return fmt.Errorf("maximum protocol version: %w", err)
	}
	if compareVersion(value.GetMinimum(), value.GetMaximum()) > 0 {
		return errors.New("protocol range minimum exceeds maximum")
	}
	if value.GetMinimum().GetMajor() != value.GetMaximum().GetMajor() {
		return errors.New("one protocol range cannot cross major versions")
	}
	return nil
}

// NegotiateProtocol selects the greatest version in both inclusive ranges.
func NegotiateProtocol(client, server *ProtocolRange) (*ProtocolVersion, *Status) {
	if err := ValidateProtocolRange(client); err != nil {
		return nil, invalidArgumentStatus(err.Error())
	}
	if err := ValidateProtocolRange(server); err != nil {
		return nil, internalStatus("server protocol range is invalid")
	}
	minimum := maxVersion(client.GetMinimum(), server.GetMinimum())
	maximum := minVersion(client.GetMaximum(), server.GetMaximum())
	if minimum.GetMajor() != maximum.GetMajor() || compareVersion(minimum, maximum) > 0 {
		return nil, &Status{
			Code:    ErrorCode_ERROR_CODE_INCOMPATIBLE_VERSION,
			Message: "client and server protocol ranges do not overlap",
			Detail: &Status_VersionMismatch{VersionMismatch: &VersionMismatch{
				Client: cloneRange(client),
				Server: cloneRange(server),
			}},
		}
	}
	return clone(maximum), OKStatus()
}

// ValidateCapabilities requires bounded, sorted, unique capability names.
func ValidateCapabilities(value *CapabilitySet) error {
	if value == nil {
		return errors.New("capability set is required")
	}
	if len(value.GetNames()) > 1024 {
		return errors.New("capability count exceeds 1024")
	}
	for index, name := range value.GetNames() {
		if err := validateToken("capability", name, maximumTokenBytes); err != nil {
			return fmt.Errorf("capability %d: %w", index, err)
		}
		if index > 0 && value.GetNames()[index-1] >= name {
			return errors.New("capabilities must be sorted and unique")
		}
	}
	return nil
}

// NegotiateCapabilities selects supported client capabilities and fails when a
// required client capability is unavailable.
func NegotiateCapabilities(
	clientSupported,
	clientRequired,
	serverSupported *CapabilitySet,
) (*CapabilitySet, *Status) {
	for _, candidate := range []struct {
		label string
		value *CapabilitySet
	}{
		{"client supported", clientSupported},
		{"client required", clientRequired},
		{"server supported", serverSupported},
	} {
		if err := ValidateCapabilities(candidate.value); err != nil {
			return nil, invalidArgumentStatus(candidate.label + " capabilities: " + err.Error())
		}
	}
	if !isSubset(clientRequired.GetNames(), clientSupported.GetNames()) {
		return nil, invalidArgumentStatus("required capabilities must be client-supported")
	}
	enabled := intersection(clientSupported.GetNames(), serverSupported.GetNames())
	missing := difference(clientRequired.GetNames(), serverSupported.GetNames())
	if len(missing) != 0 {
		return nil, &Status{
			Code:    ErrorCode_ERROR_CODE_MISSING_CAPABILITY,
			Message: "required client capabilities are unavailable",
			Detail: &Status_CapabilityMismatch{CapabilityMismatch: &CapabilityMismatch{
				Required:  slices.Clone(clientRequired.GetNames()),
				Available: slices.Clone(serverSupported.GetNames()),
				Missing:   missing,
			}},
		}
	}
	return &CapabilitySet{Names: enabled}, OKStatus()
}

// ValidateLimits rejects zero or internally inconsistent negotiated bounds.
func ValidateLimits(value *Limits) error {
	if value == nil {
		return errors.New("protocol limits are required")
	}
	if value.GetMaxMessageBytes() == 0 || value.GetMaxCollectionItems() == 0 ||
		value.GetMaxReplayEvents() == 0 || value.GetMaxReplayBytes() == 0 ||
		value.GetMaxConcurrentStreams() == 0 || value.GetMaxActiveRuns() == 0 {
		return errors.New("protocol limits must all be positive")
	}
	if uint64(value.GetMaxReplayEvents()) > value.GetMaxReplayBytes() {
		return errors.New("replay event count cannot exceed the replay byte bound")
	}
	return nil
}

// NegotiateLimits selects the lower positive bound for every resource.
func NegotiateLimits(requested, available *Limits) (*Limits, *Status) {
	if err := ValidateLimits(requested); err != nil {
		return nil, invalidArgumentStatus("requested limits: " + err.Error())
	}
	if err := ValidateLimits(available); err != nil {
		return nil, internalStatus("server limits are invalid")
	}
	return &Limits{
		MaxMessageBytes:      min(requested.GetMaxMessageBytes(), available.GetMaxMessageBytes()),
		MaxCollectionItems:   min(requested.GetMaxCollectionItems(), available.GetMaxCollectionItems()),
		MaxReplayEvents:      min(requested.GetMaxReplayEvents(), available.GetMaxReplayEvents()),
		MaxReplayBytes:       min(requested.GetMaxReplayBytes(), available.GetMaxReplayBytes()),
		MaxConcurrentStreams: min(requested.GetMaxConcurrentStreams(), available.GetMaxConcurrentStreams()),
		MaxActiveRuns:        min(requested.GetMaxActiveRuns(), available.GetMaxActiveRuns()),
	}, OKStatus()
}

// ValidateBuildIdentity rejects incomplete or unbounded build provenance.
func ValidateBuildIdentity(value *BuildIdentity) error {
	if value == nil {
		return errors.New("build identity is required")
	}
	for _, field := range [][2]string{
		{"component", value.GetComponent()},
		{"version", value.GetVersion()},
		{"commit", value.GetCommit()},
		{"Go version", value.GetGoVersion()},
	} {
		if err := validateToken(field[0], field[1], maximumTokenBytes); err != nil {
			return err
		}
	}
	return nil
}

// ValidateHealth rejects unsafe, unbounded health responses.
func ValidateHealth(value *Health) error {
	if value == nil {
		return errors.New("health is required")
	}
	switch value.GetState() {
	case HealthState_HEALTH_STATE_STARTING,
		HealthState_HEALTH_STATE_READY,
		HealthState_HEALTH_STATE_DEGRADED,
		HealthState_HEALTH_STATE_STOPPING:
	default:
		return fmt.Errorf("health state %d is unsupported", value.GetState())
	}
	if len(value.GetDegradedReasons()) > 64 {
		return errors.New("degraded reason count exceeds 64")
	}
	if value.GetState() == HealthState_HEALTH_STATE_DEGRADED && len(value.GetDegradedReasons()) == 0 {
		return errors.New("degraded health requires at least one reason")
	}
	if value.GetState() != HealthState_HEALTH_STATE_DEGRADED && len(value.GetDegradedReasons()) != 0 {
		return errors.New("only degraded health may contain degraded reasons")
	}
	for index, reason := range value.GetDegradedReasons() {
		if err := validateToken("degraded reason", reason, maximumStatusBytes); err != nil {
			return fmt.Errorf("degraded reason %d: %w", index, err)
		}
		if index > 0 && value.GetDegradedReasons()[index-1] >= reason {
			return errors.New("degraded reasons must be sorted and unique")
		}
	}
	if err := ValidateLimits(value.GetLimits()); err != nil {
		return err
	}
	if value.GetActiveRuns() > uint64(value.GetLimits().GetMaxActiveRuns()) {
		return errors.New("active run count exceeds the configured limit")
	}
	return nil
}

// ValidateStatus enforces a typed detail for errors that require recovery data.
func ValidateStatus(value *Status) error {
	if value == nil {
		return errors.New("protocol status is required")
	}
	if value.GetCode() == ErrorCode_ERROR_CODE_OK {
		if value.GetMessage() != "" || value.GetRetryable() || value.GetOperationId() != "" || value.GetDetail() != nil {
			return errors.New("successful protocol status contains error metadata")
		}
		return nil
	}
	if value.GetCode() == ErrorCode_ERROR_CODE_UNSPECIFIED {
		return errors.New("protocol status code is unspecified")
	}
	if !knownErrorCode(value.GetCode()) {
		return fmt.Errorf("protocol status code %d is unsupported", value.GetCode())
	}
	if err := validateToken("status message", value.GetMessage(), maximumStatusBytes); err != nil {
		return err
	}
	if value.GetOperationId() != "" {
		if err := validateToken("operation ID", value.GetOperationId(), 128); err != nil {
			return err
		}
	}
	return validateStatusDetail(value)
}

// ValidateEncodedSize applies the negotiated encoded-message bound.
func ValidateEncodedSize(value proto.Message, maximum uint64) error {
	if value == nil || maximum == 0 {
		return errors.New("message and positive encoded-size bound are required")
	}
	size := proto.Size(value)
	if size < 0 {
		return errors.New("encoded message size is invalid")
	}
	// #nosec G115 -- the explicit non-negative guard makes every int value safe in uint64.
	encodedSize := uint64(size)
	if encodedSize > maximum {
		return fmt.Errorf("encoded message size %d exceeds %d", encodedSize, maximum)
	}
	return nil
}

// StatusError is a safe typed protocol error independent of gRPC status text.
type StatusError struct {
	status *Status
}

// Error returns the safe protocol message.
func (current *StatusError) Error() string {
	if current == nil || current.status == nil {
		return "protocol status is unavailable"
	}
	return current.status.GetMessage()
}

// Status returns a defensive copy of the typed status.
func (current *StatusError) Status() *Status {
	if current == nil || current.status == nil {
		return nil
	}
	return clone(current.status)
}

// AsError converts a validated non-success status to an error.
func AsError(value *Status) error {
	if err := ValidateStatus(value); err != nil {
		return fmt.Errorf("invalid protocol status: %w", err)
	}
	if value.GetCode() == ErrorCode_ERROR_CODE_OK {
		return nil
	}
	return &StatusError{status: clone(value)}
}

func validateProtocolVersion(value *ProtocolVersion) error {
	if value == nil || value.GetMajor() == 0 {
		return errors.New("protocol version requires a positive major")
	}
	return nil
}

func compareVersion(left, right *ProtocolVersion) int {
	for _, pair := range [][2]uint32{
		{left.GetMajor(), right.GetMajor()},
		{left.GetMinor(), right.GetMinor()},
		{left.GetPatch(), right.GetPatch()},
	} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

func maxVersion(left, right *ProtocolVersion) *ProtocolVersion {
	if compareVersion(left, right) >= 0 {
		return left
	}
	return right
}

func minVersion(left, right *ProtocolVersion) *ProtocolVersion {
	if compareVersion(left, right) <= 0 {
		return left
	}
	return right
}

func cloneRange(value *ProtocolRange) *ProtocolRange {
	return clone(value)
}

func intersection(left, right []string) []string {
	result := make([]string, 0, min(len(left), len(right)))
	for _, candidate := range left {
		if _, found := slices.BinarySearch(right, candidate); found {
			result = append(result, candidate)
		}
	}
	return result
}

func difference(left, right []string) []string {
	result := make([]string, 0)
	for _, candidate := range left {
		if _, found := slices.BinarySearch(right, candidate); !found {
			result = append(result, candidate)
		}
	}
	return result
}

func isSubset(subset, superset []string) bool {
	return len(difference(subset, superset)) == 0
}

func validateToken(label, value string, maximum int) error {
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

func validateStatusDetail(value *Status) error {
	switch value.GetCode() {
	case ErrorCode_ERROR_CODE_INCOMPATIBLE_VERSION:
		return validateVersionMismatchDetail(value)
	case ErrorCode_ERROR_CODE_MISSING_CAPABILITY:
		return validateCapabilityMismatchDetail(value)
	case ErrorCode_ERROR_CODE_OUT_OF_RANGE:
		return validateReplayBoundsDetail(value)
	case ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED:
		return validateOverloadDetail(value)
	case ErrorCode_ERROR_CODE_STALE_CLIENT:
		return validateStaleClientDetail(value)
	case ErrorCode_ERROR_CODE_SNAPSHOT_VERSION_MISMATCH:
		return validateSnapshotVersionMismatchDetail(value)
	case ErrorCode_ERROR_CODE_UNCERTAIN_OPERATION:
		return validateUncertainOperationDetail(value)
	default:
		if value.GetDetail() != nil {
			return fmt.Errorf("protocol status %s has an unrelated typed detail", value.GetCode())
		}
		return nil
	}
}

func validateVersionMismatchDetail(value *Status) error {
	detail, ok := value.GetDetail().(*Status_VersionMismatch)
	if !ok || detail.VersionMismatch == nil {
		return requiredDetail(value)
	}
	return validateVersionMismatch(detail.VersionMismatch)
}

func validateCapabilityMismatchDetail(value *Status) error {
	detail, ok := value.GetDetail().(*Status_CapabilityMismatch)
	if !ok || detail.CapabilityMismatch == nil {
		return requiredDetail(value)
	}
	return validateCapabilityMismatch(detail.CapabilityMismatch)
}

func validateReplayBoundsDetail(value *Status) error {
	detail, ok := value.GetDetail().(*Status_ReplayBounds)
	if !ok || detail.ReplayBounds == nil {
		return requiredDetail(value)
	}
	return validateReplayBounds(detail.ReplayBounds)
}

func validateOverloadDetail(value *Status) error {
	detail, ok := value.GetDetail().(*Status_Overload)
	if !ok || detail.Overload == nil {
		return requiredDetail(value)
	}
	if err := validateToken("overload resource", detail.Overload.GetResource(), maximumTokenBytes); err != nil {
		return err
	}
	if detail.Overload.GetLimit() == 0 || detail.Overload.GetObserved() <= detail.Overload.GetLimit() {
		return errors.New("overload detail requires a positive exceeded limit")
	}
	return nil
}

func validateStaleClientDetail(value *Status) error {
	detail, ok := value.GetDetail().(*Status_StaleClient)
	if !ok || detail.StaleClient == nil {
		return requiredDetail(value)
	}
	if detail.StaleClient.GetExpectedEpoch() == 0 ||
		detail.StaleClient.GetExpectedEpoch() == detail.StaleClient.GetObservedEpoch() {
		return errors.New("stale-client detail requires distinct observed and positive expected epochs")
	}
	return nil
}

func validateSnapshotVersionMismatchDetail(value *Status) error {
	detail, ok := value.GetDetail().(*Status_SnapshotVersionMismatch)
	if !ok || detail.SnapshotVersionMismatch == nil {
		return requiredDetail(value)
	}
	if err := validateToken("expected snapshot format", detail.SnapshotVersionMismatch.GetExpected(), maximumTokenBytes); err != nil {
		return err
	}
	if err := validateToken("observed snapshot format", detail.SnapshotVersionMismatch.GetObserved(), maximumTokenBytes); err != nil {
		return err
	}
	if detail.SnapshotVersionMismatch.GetExpected() == detail.SnapshotVersionMismatch.GetObserved() {
		return errors.New("snapshot-version detail requires distinct formats")
	}
	return nil
}

func validateUncertainOperationDetail(value *Status) error {
	detail, ok := value.GetDetail().(*Status_UncertainOperation)
	if !ok || detail.UncertainOperation == nil {
		return requiredDetail(value)
	}
	if err := validateToken("uncertain operation ID", detail.UncertainOperation.GetOperationId(), 128); err != nil {
		return err
	}
	return validateToken("uncertain operation kind", detail.UncertainOperation.GetOperationKind(), maximumTokenBytes)
}

func requiredDetail(value *Status) error {
	return fmt.Errorf("protocol status %s requires its typed detail", value.GetCode())
}

func validateVersionMismatch(value *VersionMismatch) error {
	if err := ValidateProtocolRange(value.GetClient()); err != nil {
		return fmt.Errorf("version-mismatch client range: %w", err)
	}
	if err := ValidateProtocolRange(value.GetServer()); err != nil {
		return fmt.Errorf("version-mismatch server range: %w", err)
	}
	minimum := maxVersion(value.GetClient().GetMinimum(), value.GetServer().GetMinimum())
	maximum := minVersion(value.GetClient().GetMaximum(), value.GetServer().GetMaximum())
	if minimum.GetMajor() == maximum.GetMajor() && compareVersion(minimum, maximum) <= 0 {
		return errors.New("version-mismatch ranges unexpectedly overlap")
	}
	return nil
}

func validateCapabilityMismatch(value *CapabilityMismatch) error {
	for _, field := range []struct {
		label string
		names []string
	}{
		{"required", value.GetRequired()},
		{"available", value.GetAvailable()},
		{"missing", value.GetMissing()},
	} {
		if err := ValidateCapabilities(&CapabilitySet{Names: field.names}); err != nil {
			return fmt.Errorf("capability-mismatch %s: %w", field.label, err)
		}
	}
	want := difference(value.GetRequired(), value.GetAvailable())
	if len(want) == 0 || !slices.Equal(want, value.GetMissing()) {
		return errors.New("capability-mismatch missing capabilities are inconsistent")
	}
	return nil
}

func validateReplayBounds(value *ReplayBounds) error {
	if value.GetEarliestSequence() == 0 || value.GetLatestSequence() < value.GetEarliestSequence() {
		return errors.New("replay bounds contain an invalid retained window")
	}
	requested := value.GetRequestedAfterSequence()
	if requested != ^uint64(0) && requested+1 >= value.GetEarliestSequence() &&
		requested <= value.GetLatestSequence() {
		return errors.New("replay bounds unexpectedly contain the requested cursor")
	}
	if requested < value.GetEarliestSequence() {
		if value.GetRecoverySequence()+1 != value.GetEarliestSequence() {
			return errors.New("replay bounds contain an invalid earliest recovery cursor")
		}
	} else if value.GetRecoverySequence() != value.GetLatestSequence() {
		return errors.New("replay bounds contain an invalid latest recovery cursor")
	}
	return nil
}

func knownErrorCode(code ErrorCode) bool {
	return code >= ErrorCode_ERROR_CODE_OK && code <= ErrorCode_ERROR_CODE_INTERNAL
}

func invalidArgumentStatus(message string) *Status {
	return &Status{Code: ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: message}
}

func internalStatus(message string) *Status {
	return &Status{Code: ErrorCode_ERROR_CODE_INTERNAL, Message: message}
}

func clone[T proto.Message](value T) T {
	result, ok := proto.Clone(value).(T)
	if !ok {
		panic("protobuf clone changed concrete message type")
	}
	return result
}
