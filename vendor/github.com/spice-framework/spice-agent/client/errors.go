package client

import (
	"errors"
	"fmt"
	"slices"
)

// ErrorFacts are the common, safe fields carried by every failed protocol
// status. Values are immutable and contain no protocol or transport types.
// Message is server-declared safe text and must never contain StructuredValue
// application data.
type ErrorFacts struct {
	message      string
	retryable    bool
	operation    OperationID
	hasOperation bool
}

// NewErrorFacts validates and defensively copies common failed-status fields.
func NewErrorFacts(message string, retryable bool, operation *OperationID) (ErrorFacts, error) {
	if err := token("status message", message, maximumStatusBytes); err != nil {
		return ErrorFacts{}, err
	}
	result := ErrorFacts{message: message, retryable: retryable}
	if operation != nil {
		if err := operation.Validate(); err != nil {
			return ErrorFacts{}, err
		}
		result.operation = *operation
		result.hasOperation = true
	}
	return result, nil
}

func (facts ErrorFacts) Message() string { return facts.message }
func (facts ErrorFacts) Retryable() bool { return facts.retryable }
func (facts ErrorFacts) Operation() (OperationID, bool) {
	return facts.operation, facts.hasOperation
}

func (facts ErrorFacts) Validate() error {
	operation := (*OperationID)(nil)
	if facts.hasOperation {
		operation = &facts.operation
	}
	validated, err := NewErrorFacts(facts.message, facts.retryable, operation)
	if err != nil {
		return err
	}
	if validated != facts {
		return errors.New("error facts are inconsistent")
	}
	return nil
}

// StatusFailure is implemented by every typed client status error. It exposes
// the common safe fields without duplicating transport-specific status values.
type StatusFailure interface {
	error
	Facts() ErrorFacts
	Retryable() bool
	Operation() (OperationID, bool)
}

// InitializationReplayError reports a protocol-1.3 initialization whose
// response was not observed after the request may have committed. Recovery is
// safe only by replaying the same immutable request carrying AttemptID; callers
// must never create a replacement attempt for this outcome.
type InitializationReplayError struct {
	facts   ErrorFacts
	attempt InitializationAttemptID
}

// NewInitializationReplayError constructs one constrained initialization
// recovery result.
func NewInitializationReplayError(
	facts ErrorFacts,
	attempt InitializationAttemptID,
) (*InitializationReplayError, error) {
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	if !facts.Retryable() {
		return nil, errors.New("initialization replay facts must permit the exact replay")
	}
	if _, present := facts.Operation(); present {
		return nil, errors.New("initialization replay facts must not carry an operation ID")
	}
	if err := attempt.Validate(); err != nil {
		return nil, err
	}
	return &InitializationReplayError{facts: facts, attempt: attempt}, nil
}

func (current *InitializationReplayError) Error() string {
	return errorMessage(current.Facts(), current != nil)
}

func (current *InitializationReplayError) Facts() ErrorFacts {
	if current == nil {
		return ErrorFacts{}
	}
	return current.facts
}

func (current *InitializationReplayError) Retryable() bool {
	return retryable(current.Facts(), current != nil)
}

func (current *InitializationReplayError) Operation() (OperationID, bool) {
	return operation(current.Facts(), current != nil)
}

// AttemptID returns the exact caller-owned identity that must be reused.
func (current *InitializationReplayError) AttemptID() InitializationAttemptID {
	if current == nil {
		return InitializationAttemptID{}
	}
	return current.attempt
}

func errorMessage(facts ErrorFacts, available bool) string {
	if !available {
		return "client status is unavailable"
	}
	return facts.message
}

func retryable(facts ErrorFacts, available bool) bool {
	return available && facts.retryable
}

func operation(facts ErrorFacts, available bool) (OperationID, bool) {
	if !available {
		return OperationID{}, false
	}
	return facts.operation, facts.hasOperation
}

// TerminalError reports that a requested mutation targets an already-terminal
// run. It preserves the full common status alongside the stable run detail.
type TerminalError struct {
	facts ErrorFacts
	run   RunRef
}

func NewTerminalError(facts ErrorFacts, run RunRef) (*TerminalError, error) {
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	if err := run.Validate(); err != nil {
		return nil, err
	}
	return &TerminalError{facts: facts, run: run}, nil
}

func (current *TerminalError) Error() string {
	return errorMessage(current.Facts(), current != nil)
}

func (current *TerminalError) Facts() ErrorFacts {
	if current == nil {
		return ErrorFacts{}
	}
	return current.facts
}

func (current *TerminalError) Retryable() bool {
	return retryable(current.Facts(), current != nil)
}

func (current *TerminalError) Operation() (OperationID, bool) {
	return operation(current.Facts(), current != nil)
}

func (current *TerminalError) Run() RunRef {
	if current == nil {
		return RunRef{}
	}
	return current.run
}

// UncertainOperationError reports a mutation whose commit outcome cannot be
// determined safely. Callers must inspect authoritative state before deciding
// whether a new operation is safe; implementations never replay it implicitly.
type UncertainOperationError struct {
	facts              ErrorFacts
	uncertainOperation OperationID
	kind               string
}

func NewUncertainOperationError(
	facts ErrorFacts,
	uncertainOperation OperationID,
	kind string,
) (*UncertainOperationError, error) {
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	if err := uncertainOperation.Validate(); err != nil {
		return nil, err
	}
	if err := token("uncertain operation kind", kind, maximumTokenBytes); err != nil {
		return nil, err
	}
	return &UncertainOperationError{facts: facts, uncertainOperation: uncertainOperation, kind: kind}, nil
}

func (current *UncertainOperationError) Error() string {
	return errorMessage(current.Facts(), current != nil)
}

func (current *UncertainOperationError) Facts() ErrorFacts {
	if current == nil {
		return ErrorFacts{}
	}
	return current.facts
}

func (current *UncertainOperationError) Retryable() bool {
	return retryable(current.Facts(), current != nil)
}

func (current *UncertainOperationError) Operation() (OperationID, bool) {
	return operation(current.Facts(), current != nil)
}

func (current *UncertainOperationError) UncertainOperation() OperationID {
	if current == nil {
		return OperationID{}
	}
	return current.uncertainOperation
}

func (current *UncertainOperationError) Kind() string {
	if current == nil {
		return ""
	}
	return current.kind
}

// CursorGapError reports that acknowledged history is no longer retained.
// RecoverySequence is the explicit server-selected cursor for resynchronizing.
type CursorGapError struct {
	facts     ErrorFacts
	run       RunRef
	requested uint64
	earliest  uint64
	latest    uint64
	recovery  uint64
}

func NewCursorGapError(
	facts ErrorFacts,
	run RunRef,
	requested, earliest, latest, recovery uint64,
) (*CursorGapError, error) {
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	if err := run.Validate(); err != nil {
		return nil, err
	}
	if earliest == 0 || latest < earliest {
		return nil, errors.New("cursor gap bounds are invalid")
	}
	tooOld := requested < earliest-1 && recovery == earliest-1
	tooNew := requested > latest && recovery == latest
	if !tooOld && !tooNew {
		return nil, errors.New("cursor gap bounds are invalid")
	}
	return &CursorGapError{
		facts: facts, run: run, requested: requested, earliest: earliest, latest: latest, recovery: recovery,
	}, nil
}

func (current *CursorGapError) Error() string {
	return errorMessage(current.Facts(), current != nil)
}

func (current *CursorGapError) Facts() ErrorFacts {
	if current == nil {
		return ErrorFacts{}
	}
	return current.facts
}

func (current *CursorGapError) Retryable() bool {
	return retryable(current.Facts(), current != nil)
}

func (current *CursorGapError) Operation() (OperationID, bool) {
	return operation(current.Facts(), current != nil)
}

func (current *CursorGapError) Run() RunRef {
	if current == nil {
		return RunRef{}
	}
	return current.run
}

func (current *CursorGapError) RequestedAfterSequence() uint64 {
	if current == nil {
		return 0
	}
	return current.requested
}

func (current *CursorGapError) EarliestSequence() uint64 {
	if current == nil {
		return 0
	}
	return current.earliest
}

func (current *CursorGapError) LatestSequence() uint64 {
	if current == nil {
		return 0
	}
	return current.latest
}

func (current *CursorGapError) RecoverySequence() uint64 {
	if current == nil {
		return 0
	}
	return current.recovery
}

// StaleSessionError reports a lost stable-client ownership epoch. The caller
// may reconnect using the expected epoch only after reconciling ownership.
type StaleSessionError struct {
	facts    ErrorFacts
	expected uint64
	observed uint64
}

func NewStaleSessionError(facts ErrorFacts, expected, observed uint64) (*StaleSessionError, error) {
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	if expected == 0 || expected == observed {
		return nil, errors.New("stale session epochs require a positive expected value different from observed")
	}
	return &StaleSessionError{facts: facts, expected: expected, observed: observed}, nil
}

func (current *StaleSessionError) Error() string {
	return errorMessage(current.Facts(), current != nil)
}

func (current *StaleSessionError) Facts() ErrorFacts {
	if current == nil {
		return ErrorFacts{}
	}
	return current.facts
}

func (current *StaleSessionError) Retryable() bool {
	return retryable(current.Facts(), current != nil)
}

func (current *StaleSessionError) Operation() (OperationID, bool) {
	return operation(current.Facts(), current != nil)
}

func (current *StaleSessionError) ExpectedEpoch() uint64 {
	if current == nil {
		return 0
	}
	return current.expected
}

func (current *StaleSessionError) ObservedEpoch() uint64 {
	if current == nil {
		return 0
	}
	return current.observed
}

// VersionMismatchError reports two valid protocol ranges that do not overlap.
type VersionMismatchError struct {
	facts  ErrorFacts
	client ProtocolRange
	server ProtocolRange
}

func NewVersionMismatchError(
	facts ErrorFacts,
	client, server ProtocolRange,
) (*VersionMismatchError, error) {
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	if err := client.Validate(); err != nil {
		return nil, fmt.Errorf("client protocol range: %w", err)
	}
	if err := server.Validate(); err != nil {
		return nil, fmt.Errorf("server protocol range: %w", err)
	}
	if protocolRangesOverlap(client, server) {
		return nil, errors.New("version-mismatch protocol ranges unexpectedly overlap")
	}
	return &VersionMismatchError{facts: facts, client: client, server: server}, nil
}

func (current *VersionMismatchError) Error() string {
	return errorMessage(current.Facts(), current != nil)
}

func (current *VersionMismatchError) Facts() ErrorFacts {
	if current == nil {
		return ErrorFacts{}
	}
	return current.facts
}

func (current *VersionMismatchError) Retryable() bool {
	return retryable(current.Facts(), current != nil)
}

func (current *VersionMismatchError) Operation() (OperationID, bool) {
	return operation(current.Facts(), current != nil)
}

func (current *VersionMismatchError) Client() ProtocolRange {
	if current == nil {
		return ProtocolRange{}
	}
	return current.client
}

func (current *VersionMismatchError) Server() ProtocolRange {
	if current == nil {
		return ProtocolRange{}
	}
	return current.server
}

func protocolRangesOverlap(left, right ProtocolRange) bool {
	if left.minimum.major != right.minimum.major {
		return false
	}
	minimum := left.minimum
	if compareProtocol(right.minimum, minimum) > 0 {
		minimum = right.minimum
	}
	maximum := left.maximum
	if compareProtocol(right.maximum, maximum) < 0 {
		maximum = right.maximum
	}
	return compareProtocol(minimum, maximum) <= 0
}

// CapabilityMismatchError reports the exact required capabilities unavailable
// from a server. All slices are canonical and defensively copied.
type CapabilityMismatchError struct {
	facts     ErrorFacts
	required  []string
	available []string
	missing   []string
}

func NewCapabilityMismatchError(
	facts ErrorFacts,
	required, available, missing []string,
) (*CapabilityMismatchError, error) {
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	requiredValues, err := canonicalTokens("required capability", required)
	if err != nil {
		return nil, err
	}
	availableValues, err := canonicalTokens("available capability", available)
	if err != nil {
		return nil, err
	}
	missingValues, err := canonicalTokens("missing capability", missing)
	if err != nil {
		return nil, err
	}
	want := make([]string, 0, len(requiredValues))
	for _, value := range requiredValues {
		if _, found := slices.BinarySearch(availableValues, value); !found {
			want = append(want, value)
		}
	}
	if len(want) == 0 || !slices.Equal(want, missingValues) {
		return nil, errors.New("missing capabilities are inconsistent with required and available sets")
	}
	return &CapabilityMismatchError{
		facts: facts, required: requiredValues, available: availableValues, missing: missingValues,
	}, nil
}

func (current *CapabilityMismatchError) Error() string {
	return errorMessage(current.Facts(), current != nil)
}

func (current *CapabilityMismatchError) Facts() ErrorFacts {
	if current == nil {
		return ErrorFacts{}
	}
	return current.facts
}

func (current *CapabilityMismatchError) Retryable() bool {
	return retryable(current.Facts(), current != nil)
}

func (current *CapabilityMismatchError) Operation() (OperationID, bool) {
	return operation(current.Facts(), current != nil)
}

func (current *CapabilityMismatchError) Required() []string {
	if current == nil {
		return nil
	}
	return slices.Clone(current.required)
}

func (current *CapabilityMismatchError) Available() []string {
	if current == nil {
		return nil
	}
	return slices.Clone(current.available)
}

func (current *CapabilityMismatchError) Missing() []string {
	if current == nil {
		return nil
	}
	return slices.Clone(current.missing)
}

// OverloadError reports one exceeded bounded resource.
type OverloadError struct {
	facts    ErrorFacts
	resource string
	limit    uint64
	observed uint64
}

func NewOverloadError(facts ErrorFacts, resource string, limit, observed uint64) (*OverloadError, error) {
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	if err := token("overload resource", resource, maximumTokenBytes); err != nil {
		return nil, err
	}
	if limit == 0 || observed <= limit {
		return nil, errors.New("overload requires a positive exceeded limit")
	}
	return &OverloadError{facts: facts, resource: resource, limit: limit, observed: observed}, nil
}

func (current *OverloadError) Error() string {
	return errorMessage(current.Facts(), current != nil)
}

func (current *OverloadError) Facts() ErrorFacts {
	if current == nil {
		return ErrorFacts{}
	}
	return current.facts
}

func (current *OverloadError) Retryable() bool {
	return retryable(current.Facts(), current != nil)
}

func (current *OverloadError) Operation() (OperationID, bool) {
	return operation(current.Facts(), current != nil)
}

func (current *OverloadError) Resource() string {
	if current == nil {
		return ""
	}
	return current.resource
}

func (current *OverloadError) Limit() uint64 {
	if current == nil {
		return 0
	}
	return current.limit
}

func (current *OverloadError) Observed() uint64 {
	if current == nil {
		return 0
	}
	return current.observed
}

// SnapshotVersionMismatchError reports incompatible safe snapshot formats.
type SnapshotVersionMismatchError struct {
	facts    ErrorFacts
	expected string
	observed string
}

func NewSnapshotVersionMismatchError(
	facts ErrorFacts,
	expected, observed string,
) (*SnapshotVersionMismatchError, error) {
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	if err := token("expected snapshot format", expected, maximumTokenBytes); err != nil {
		return nil, err
	}
	if err := token("observed snapshot format", observed, maximumTokenBytes); err != nil {
		return nil, err
	}
	if expected == observed {
		return nil, errors.New("snapshot formats must be different")
	}
	return &SnapshotVersionMismatchError{facts: facts, expected: expected, observed: observed}, nil
}

func (current *SnapshotVersionMismatchError) Error() string {
	return errorMessage(current.Facts(), current != nil)
}

func (current *SnapshotVersionMismatchError) Facts() ErrorFacts {
	if current == nil {
		return ErrorFacts{}
	}
	return current.facts
}

func (current *SnapshotVersionMismatchError) Retryable() bool {
	return retryable(current.Facts(), current != nil)
}

func (current *SnapshotVersionMismatchError) Operation() (OperationID, bool) {
	return operation(current.Facts(), current != nil)
}

func (current *SnapshotVersionMismatchError) Expected() string {
	if current == nil {
		return ""
	}
	return current.expected
}

func (current *SnapshotVersionMismatchError) Observed() string {
	if current == nil {
		return ""
	}
	return current.observed
}

// ErrorCode identifies a stable generic failure without exposing transport
// status types. Failures with structured recovery facts use dedicated errors.
type ErrorCode string

const (
	ErrorInvalidArgument  ErrorCode = "invalid-argument"
	ErrorUnauthenticated  ErrorCode = "unauthenticated"
	ErrorNotFound         ErrorCode = "not-found"
	ErrorUnavailable      ErrorCode = "unavailable"
	ErrorCancelled        ErrorCode = "cancelled"
	ErrorDeadlineExceeded ErrorCode = "deadline-exceeded"
	ErrorConflict         ErrorCode = "conflict"
	ErrorInternal         ErrorCode = "internal"
)

// StatusError is one immutable generic application failure.
type StatusError struct {
	facts ErrorFacts
	code  ErrorCode
}

func NewStatusError(code ErrorCode, facts ErrorFacts) (*StatusError, error) {
	if !validGenericErrorCode(code) {
		return nil, fmt.Errorf("generic error code %q is unsupported", code)
	}
	if err := facts.Validate(); err != nil {
		return nil, err
	}
	return &StatusError{facts: facts, code: code}, nil
}

func (current *StatusError) Error() string {
	return errorMessage(current.Facts(), current != nil)
}

func (current *StatusError) Facts() ErrorFacts {
	if current == nil {
		return ErrorFacts{}
	}
	return current.facts
}

func (current *StatusError) Retryable() bool {
	return retryable(current.Facts(), current != nil)
}

func (current *StatusError) Operation() (OperationID, bool) {
	return operation(current.Facts(), current != nil)
}

func (current *StatusError) Code() ErrorCode {
	if current == nil {
		return ""
	}
	return current.code
}

func validGenericErrorCode(code ErrorCode) bool {
	switch code {
	case ErrorInvalidArgument, ErrorUnauthenticated, ErrorNotFound, ErrorUnavailable,
		ErrorCancelled, ErrorDeadlineExceeded, ErrorConflict, ErrorInternal:
		return true
	default:
		return false
	}
}

var (
	_ StatusFailure = (*InitializationReplayError)(nil)
	_ StatusFailure = (*TerminalError)(nil)
	_ StatusFailure = (*UncertainOperationError)(nil)
	_ StatusFailure = (*CursorGapError)(nil)
	_ StatusFailure = (*StaleSessionError)(nil)
	_ StatusFailure = (*VersionMismatchError)(nil)
	_ StatusFailure = (*CapabilityMismatchError)(nil)
	_ StatusFailure = (*OverloadError)(nil)
	_ StatusFailure = (*SnapshotVersionMismatchError)(nil)
	_ StatusFailure = (*StatusError)(nil)
)
