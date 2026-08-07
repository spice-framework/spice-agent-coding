package client

import (
	"errors"
	"fmt"
	"strings"
)

// ReconnectClaim requests an atomic ownership-epoch compare-and-swap for a
// stable client identity. It is not an authentication credential.
type ReconnectClaim struct {
	clientID      string
	expectedEpoch uint64
}

// NewReconnectClaim constructs a reconnect ownership claim.
func NewReconnectClaim(clientID string, expectedEpoch uint64) (ReconnectClaim, error) {
	if err := token("reconnect client ID", clientID, 128); err != nil {
		return ReconnectClaim{}, err
	}
	if expectedEpoch == 0 || expectedEpoch == ^uint64(0) {
		return ReconnectClaim{}, errors.New("expected ownership epoch must be positive and incrementable")
	}
	return ReconnectClaim{clientID: clientID, expectedEpoch: expectedEpoch}, nil
}

func (claim ReconnectClaim) ClientID() string           { return claim.clientID }
func (claim ReconnectClaim) ExpectedEpoch() uint64      { return claim.expectedEpoch }
func (claim ReconnectClaim) NextOwnershipEpoch() uint64 { return claim.expectedEpoch + 1 }

func (claim ReconnectClaim) Validate() error {
	_, err := NewReconnectClaim(claim.clientID, claim.expectedEpoch)
	return err
}

// InitializeRequest is the immutable, non-secret client negotiation request.
type InitializeRequest struct {
	protocol     ProtocolRange
	client       Build
	supported    []string
	required     []string
	limits       Limits
	reconnect    ReconnectClaim
	hasReconnect bool
	attempt      InitializationAttemptID
	hasAttempt   bool
}

// NewInitializeRequest constructs a legacy fresh-owner initialization request.
// It deliberately negotiates at most protocol 1.2 and therefore cannot be
// retried safely after an uncertain transport outcome. New code that supports
// protocol 1.3 should use NewInitializeRequestWithAttempt.
//
// Deprecated: use NewInitializeRequestWithAttempt for protocol 1.3 clients.
func NewInitializeRequest(protocol ProtocolRange, client Build, supported, required []string, limits Limits) (InitializeRequest, error) {
	return NewLegacyInitializeRequest(protocol, client, supported, required, limits)
}

// NewLegacyInitializeRequest constructs a protocol-1.0-through-1.2 fresh-owner
// request whose ambiguous transport failures must never be retried.
func NewLegacyInitializeRequest(protocol ProtocolRange, client Build, supported, required []string, limits Limits) (InitializeRequest, error) {
	return newInitializeRequest(protocol, client, supported, required, limits, nil, nil)
}

// NewInitializeRequestWithAttempt constructs a protocol-1.3-only fresh-owner
// request with one caller-owned exact replay identity.
func NewInitializeRequestWithAttempt(
	protocol ProtocolRange,
	client Build,
	supported, required []string,
	limits Limits,
	attempt InitializationAttemptID,
) (InitializeRequest, error) {
	return newInitializeRequest(protocol, client, supported, required, limits, nil, &attempt)
}

// NewReconnectRequest constructs an initialization request that atomically
// reclaims one stable client identity at its expected ownership epoch using
// legacy protocol semantics. It negotiates at most protocol 1.2 and is never
// retried automatically after an uncertain transport outcome.
//
// Deprecated: use NewReconnectRequestWithAttempt for protocol 1.3 clients.
func NewReconnectRequest(protocol ProtocolRange, client Build, supported, required []string, limits Limits, claim ReconnectClaim) (InitializeRequest, error) {
	return NewLegacyReconnectRequest(protocol, client, supported, required, limits, claim)
}

// NewLegacyReconnectRequest constructs a protocol-1.0-through-1.2 reconnect
// request whose ambiguous ownership-CAS transport failures must never be
// retried.
func NewLegacyReconnectRequest(protocol ProtocolRange, client Build, supported, required []string, limits Limits, claim ReconnectClaim) (InitializeRequest, error) {
	return newInitializeRequest(protocol, client, supported, required, limits, &claim, nil)
}

// NewReconnectRequestWithAttempt constructs a protocol-1.3-only reconnect
// request with one caller-owned exact replay identity.
func NewReconnectRequestWithAttempt(
	protocol ProtocolRange,
	client Build,
	supported, required []string,
	limits Limits,
	claim ReconnectClaim,
	attempt InitializationAttemptID,
) (InitializeRequest, error) {
	return newInitializeRequest(protocol, client, supported, required, limits, &claim, &attempt)
}

func newInitializeRequest(
	protocol ProtocolRange,
	client Build,
	supported, required []string,
	limits Limits,
	claim *ReconnectClaim,
	attempt *InitializationAttemptID,
) (InitializeRequest, error) {
	if err := protocol.Validate(); err != nil {
		return InitializeRequest{}, err
	}
	var err error
	if attempt == nil {
		protocol, err = legacyInitializeProtocol(protocol)
	} else {
		protocol, err = attemptInitializeProtocol(protocol)
	}
	if err != nil {
		return InitializeRequest{}, err
	}
	if validationErr := client.Validate(); validationErr != nil {
		return InitializeRequest{}, validationErr
	}
	supportedValues, err := canonicalTokens("supported capability", supported)
	if err != nil {
		return InitializeRequest{}, err
	}
	requiredValues, err := canonicalTokens("required capability", required)
	if err != nil {
		return InitializeRequest{}, err
	}
	if !containsAll(supportedValues, requiredValues) {
		return InitializeRequest{}, errors.New("required capabilities must be supported by the client")
	}
	if validationErr := limits.Validate(); validationErr != nil {
		return InitializeRequest{}, validationErr
	}
	result := InitializeRequest{
		protocol: protocol, client: client, supported: supportedValues,
		required: requiredValues, limits: limits,
	}
	if claim != nil {
		if validationErr := claim.Validate(); validationErr != nil {
			return InitializeRequest{}, validationErr
		}
		result.reconnect = *claim
		result.hasReconnect = true
	}
	if attempt != nil {
		if validationErr := attempt.Validate(); validationErr != nil {
			return InitializeRequest{}, validationErr
		}
		result.attempt = *attempt
		result.hasAttempt = true
	}
	return result, nil
}

func legacyInitializeProtocol(protocol ProtocolRange) (ProtocolRange, error) {
	target := ProtocolVersion{
		major: initializationAttemptProtocolMajor,
		minor: initializationAttemptProtocolMinor,
	}
	if protocol.maximum.major != target.major || compareProtocol(protocol.maximum, target) < 0 {
		return protocol, nil
	}
	legacyMaximum := ProtocolVersion{major: target.major, minor: target.minor - 1}
	if compareProtocol(protocol.minimum, legacyMaximum) > 0 {
		return ProtocolRange{}, errors.New("protocol 1.3 initialization requires an initialization attempt ID")
	}
	return ProtocolRange{minimum: protocol.minimum, maximum: legacyMaximum}, nil
}

func attemptInitializeProtocol(protocol ProtocolRange) (ProtocolRange, error) {
	target := ProtocolVersion{
		major: initializationAttemptProtocolMajor,
		minor: initializationAttemptProtocolMinor,
	}
	if compareProtocol(protocol.minimum, target) > 0 || compareProtocol(protocol.maximum, target) < 0 {
		return ProtocolRange{}, errors.New("initialization attempt ID requires protocol 1.3 support")
	}
	return ProtocolRange{minimum: target, maximum: target}, nil
}

func (request InitializeRequest) Protocol() ProtocolRange { return request.protocol }
func (request InitializeRequest) Client() Build           { return request.client }
func (request InitializeRequest) SupportedCapabilities() []string {
	return append([]string(nil), request.supported...)
}

func (request InitializeRequest) RequiredCapabilities() []string {
	return append([]string(nil), request.required...)
}
func (request InitializeRequest) RequestedLimits() Limits { return request.limits }
func (request InitializeRequest) Reconnect() (ReconnectClaim, bool) {
	return request.reconnect, request.hasReconnect
}

// AttemptID returns the caller-owned exact replay identity when this is a
// protocol-1.3 initialization request.
func (request InitializeRequest) AttemptID() (InitializationAttemptID, bool) {
	return request.attempt, request.hasAttempt
}

func (request InitializeRequest) Validate() error {
	var claim *ReconnectClaim
	if request.hasReconnect {
		claim = &request.reconnect
	}
	var attempt *InitializationAttemptID
	if request.hasAttempt {
		attempt = &request.attempt
	}
	_, err := newInitializeRequest(
		request.protocol,
		request.client,
		request.supported,
		request.required,
		request.limits,
		claim,
		attempt,
	)
	return err
}

// OperationID is a caller-generated idempotency identity for one mutation.
type OperationID struct{ value string }

// NewOperationID constructs a bounded mutation identity.
func NewOperationID(value string) (OperationID, error) {
	if err := token("client operation ID", value, 128); err != nil {
		return OperationID{}, err
	}
	return OperationID{value: value}, nil
}

func (id OperationID) String() string { return id.value }
func (id OperationID) Validate() error {
	_, err := NewOperationID(id.value)
	return err
}

// RunRef is one validated stable run identity. It carries no hidden session or
// network state and may be reconstructed when reconnecting to a known run.
type RunRef struct{ id string }

// NewRunRef constructs a stable run reference.
func NewRunRef(id string) (RunRef, error) {
	if err := token("run ID", id, 128); err != nil {
		return RunRef{}, err
	}
	return RunRef{id: id}, nil
}

func (run RunRef) ID() string { return run.id }
func (run RunRef) Validate() error {
	_, err := NewRunRef(run.id)
	return err
}

// Input is the bounded initial user text for an architecture-proof run. Rich
// content is intentionally deferred so it can be added as typed variants.
type Input struct {
	messageID string
	text      string
}

// NewInput constructs one initial user text message.
func NewInput(messageID, text string) (Input, error) {
	if err := token("input message ID", messageID, 128); err != nil {
		return Input{}, err
	}
	if err := boundedText("input text", text, MaximumTextBytes, false); err != nil {
		return Input{}, err
	}
	if strings.TrimSpace(text) == "" {
		return Input{}, errors.New("input text must contain non-whitespace content")
	}
	return Input{messageID: messageID, text: text}, nil
}

func (input Input) MessageID() string { return input.messageID }
func (input Input) Text() string      { return input.text }
func (input Input) Validate() error {
	_, err := NewInput(input.messageID, input.text)
	return err
}

// StartRequest is one idempotent generated-definition run request.
type StartRequest struct {
	operation  OperationID
	definition DefinitionRef
	input      Input
}

func NewStartRequest(operation OperationID, definition DefinitionRef, input Input) (StartRequest, error) {
	if err := operation.Validate(); err != nil {
		return StartRequest{}, err
	}
	if err := definition.Validate(); err != nil {
		return StartRequest{}, err
	}
	if err := input.Validate(); err != nil {
		return StartRequest{}, err
	}
	return StartRequest{operation: operation, definition: definition, input: input}, nil
}

func (request StartRequest) Operation() OperationID    { return request.operation }
func (request StartRequest) Definition() DefinitionRef { return request.definition }
func (request StartRequest) Input() Input              { return request.input }

func (request StartRequest) Validate() error {
	_, err := NewStartRequest(request.operation, request.definition, request.input)
	return err
}

// StartResult identifies the server-selected immutable plan and first sequence.
type StartResult struct {
	run             RunRef
	initialSequence uint64
	planID          string
	duplicate       bool
}

func NewStartResult(run RunRef, initialSequence uint64, planID string, duplicate bool) (StartResult, error) {
	if err := run.Validate(); err != nil {
		return StartResult{}, err
	}
	if initialSequence == 0 {
		return StartResult{}, errors.New("initial run sequence must be positive")
	}
	if err := token("run plan ID", planID, maximumTokenBytes); err != nil {
		return StartResult{}, err
	}
	return StartResult{run: run, initialSequence: initialSequence, planID: planID, duplicate: duplicate}, nil
}

func (result StartResult) Run() RunRef              { return result.run }
func (result StartResult) InitialSequence() uint64  { return result.initialSequence }
func (result StartResult) PlanID() string           { return result.planID }
func (result StartResult) DuplicateOperation() bool { return result.duplicate }

// Cursor is a caller-acknowledged run position. A zero sequence means no event
// has been acknowledged yet.
type Cursor struct {
	run           RunRef
	afterSequence uint64
}

func NewCursor(run RunRef, afterSequence uint64) (Cursor, error) {
	if err := run.Validate(); err != nil {
		return Cursor{}, err
	}
	return Cursor{run: run, afterSequence: afterSequence}, nil
}

func (cursor Cursor) Run() RunRef           { return cursor.run }
func (cursor Cursor) AfterSequence() uint64 { return cursor.afterSequence }

func (cursor Cursor) Validate() error {
	_, err := NewCursor(cursor.run, cursor.afterSequence)
	return err
}

// EventStreamOptions bounds one replay page and chooses whether to tail.
type EventStreamOptions struct {
	replayLimit uint32
	tail        bool
}

func NewEventStreamOptions(replayLimit uint32, tail bool, limits Limits) (EventStreamOptions, error) {
	if err := limits.Validate(); err != nil {
		return EventStreamOptions{}, err
	}
	if replayLimit == 0 || replayLimit > limits.ReplayEvents() {
		return EventStreamOptions{}, fmt.Errorf("event replay limit must be between 1 and %d", limits.ReplayEvents())
	}
	return EventStreamOptions{replayLimit: replayLimit, tail: tail}, nil
}

func (options EventStreamOptions) ReplayLimit() uint32 { return options.replayLimit }
func (options EventStreamOptions) Tail() bool          { return options.tail }

func (options EventStreamOptions) Validate(limits Limits) error {
	_, err := NewEventStreamOptions(options.replayLimit, options.tail, limits)
	return err
}

// CancelResult reports an idempotent cooperative cancellation outcome.
type CancelResult struct {
	requested        bool
	alreadyTerminal  bool
	terminalSequence uint64
}

func NewCancelResult(requested, alreadyTerminal bool, terminalSequence uint64) (CancelResult, error) {
	if requested == alreadyTerminal {
		return CancelResult{}, errors.New("cancel result must report exactly one outcome")
	}
	if alreadyTerminal != (terminalSequence > 0) {
		return CancelResult{}, errors.New("terminal cancellation outcome requires its terminal sequence")
	}
	return CancelResult{requested: requested, alreadyTerminal: alreadyTerminal, terminalSequence: terminalSequence}, nil
}

func (result CancelResult) Requested() bool          { return result.requested }
func (result CancelResult) AlreadyTerminal() bool    { return result.alreadyTerminal }
func (result CancelResult) TerminalSequence() uint64 { return result.terminalSequence }

// SuspendResult reports an idempotent safe-boundary suspension.
type SuspendResult struct {
	suspended        bool
	boundarySequence uint64
	duplicate        bool
}

func NewSuspendResult(suspended bool, boundarySequence uint64, duplicate bool) (SuspendResult, error) {
	if !suspended || boundarySequence == 0 {
		return SuspendResult{}, errors.New("suspend result requires a committed positive boundary")
	}
	return SuspendResult{suspended: true, boundarySequence: boundarySequence, duplicate: duplicate}, nil
}

func (result SuspendResult) Suspended() bool          { return result.suspended }
func (result SuspendResult) BoundarySequence() uint64 { return result.boundarySequence }
func (result SuspendResult) DuplicateOperation() bool { return result.duplicate }

// ResumeResult reports a local suspended run's continuation sequence.
type ResumeResult struct {
	resumed      bool
	nextSequence uint64
	duplicate    bool
}

func NewResumeResult(resumed bool, nextSequence uint64, duplicate bool) (ResumeResult, error) {
	if !resumed || nextSequence == 0 {
		return ResumeResult{}, errors.New("resume result requires a positive continuation sequence")
	}
	return ResumeResult{resumed: true, nextSequence: nextSequence, duplicate: duplicate}, nil
}

func (result ResumeResult) Resumed() bool            { return result.resumed }
func (result ResumeResult) NextSequence() uint64     { return result.nextSequence }
func (result ResumeResult) DuplicateOperation() bool { return result.duplicate }

// ImportResult reports an authenticated snapshot import.
type ImportResult struct {
	run          RunRef
	nextSequence uint64
	duplicate    bool
}

func NewImportResult(run RunRef, nextSequence uint64, duplicate bool) (ImportResult, error) {
	if err := run.Validate(); err != nil {
		return ImportResult{}, err
	}
	if nextSequence == 0 {
		return ImportResult{}, errors.New("import continuation sequence must be positive")
	}
	return ImportResult{run: run, nextSequence: nextSequence, duplicate: duplicate}, nil
}

func (result ImportResult) Run() RunRef              { return result.run }
func (result ImportResult) NextSequence() uint64     { return result.nextSequence }
func (result ImportResult) DuplicateOperation() bool { return result.duplicate }

// CancelRequest is one idempotent cooperative cancellation mutation.
type CancelRequest struct {
	run       RunRef
	operation OperationID
	reason    string
}

func NewCancelRequest(run RunRef, operation OperationID, reason string) (CancelRequest, error) {
	if err := run.Validate(); err != nil {
		return CancelRequest{}, err
	}
	if err := operation.Validate(); err != nil {
		return CancelRequest{}, err
	}
	if reason != "" {
		if err := token("cancellation reason", reason, 1024); err != nil {
			return CancelRequest{}, err
		}
	}
	return CancelRequest{run: run, operation: operation, reason: reason}, nil
}

func (request CancelRequest) Run() RunRef            { return request.run }
func (request CancelRequest) Operation() OperationID { return request.operation }
func (request CancelRequest) Reason() string         { return request.reason }

// RunMutation is an idempotent suspend or resume request.
type RunMutation struct {
	run       RunRef
	operation OperationID
}

func NewRunMutation(run RunRef, operation OperationID) (RunMutation, error) {
	if err := run.Validate(); err != nil {
		return RunMutation{}, err
	}
	if err := operation.Validate(); err != nil {
		return RunMutation{}, err
	}
	return RunMutation{run: run, operation: operation}, nil
}

func (request RunMutation) Run() RunRef            { return request.run }
func (request RunMutation) Operation() OperationID { return request.operation }

// ImportRequest is one idempotent authenticated snapshot import.
type ImportRequest struct {
	operation OperationID
	snapshot  Snapshot
}

func NewImportRequest(operation OperationID, snapshot Snapshot) (ImportRequest, error) {
	if err := operation.Validate(); err != nil {
		return ImportRequest{}, err
	}
	if _, err := snapshot.MarshalBinary(); err != nil {
		return ImportRequest{}, err
	}
	return ImportRequest{operation: operation, snapshot: snapshot}, nil
}

func (request ImportRequest) Operation() OperationID { return request.operation }
func (request ImportRequest) Snapshot() Snapshot     { return request.snapshot }
