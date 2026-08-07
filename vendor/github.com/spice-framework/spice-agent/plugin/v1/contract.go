package pluginv1

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	"github.com/spice-framework/spice-agent/tool"
	"google.golang.org/protobuf/proto"
)

const (
	// ProtocolMajor is the initial runtime-tool plugin wire major.
	ProtocolMajor = uint32(1)
	// ProtocolMinimumMinor is the oldest compatible plugin wire minor.
	ProtocolMinimumMinor = uint32(0)
	// ProtocolMinor is the highest plugin wire minor implemented here.
	ProtocolMinor = uint32(0)
	// ProtocolPatch is the current plugin wire patch.
	ProtocolPatch = uint32(0)

	// InitializeBootstrapMaximumBytes is the pre-negotiation message bound.
	InitializeBootstrapMaximumBytes = 1 << 20
	// LaunchIDBytes is the canonical caller-generated launch identity size.
	LaunchIDBytes = 16
	// SessionIDBytes is the canonical initialized connection identity size.
	SessionIDBytes = 16
	// HandshakeChallengeBytes is the canonical random challenge size.
	HandshakeChallengeBytes = 32
	// HandshakeSecretBytes is the required per-launch secret size.
	HandshakeSecretBytes = 32
	// HandshakeProofBytes is the HMAC-SHA256 proof size.
	HandshakeProofBytes = sha256.Size
	// CapabilityRuntimeToolsV1 is required by the initial tool-only protocol.
	CapabilityRuntimeToolsV1 = "runtime-tools-v1"

	maximumTokenBytes       = 256
	maximumDescriptionBytes = tool.MaximumProgressBytes
	maximumProblemBytes     = tool.MaximumProgressBytes
	maximumToolCount        = 4096
	maximumConcurrentCalls  = 4096
)

// SupportedProtocolRange returns the exact protocol range implemented here.
func SupportedProtocolRange() *commonv1.ProtocolRange {
	return &commonv1.ProtocolRange{
		Minimum: &commonv1.ProtocolVersion{
			Major: ProtocolMajor,
			Minor: ProtocolMinimumMinor,
			Patch: ProtocolPatch,
		},
		Maximum: &commonv1.ProtocolVersion{
			Major: ProtocolMajor,
			Minor: ProtocolMinor,
			Patch: ProtocolPatch,
		},
	}
}

// ValidateLimits rejects zero, internally inconsistent, or unreasonable
// connection capacities.
func ValidateLimits(value *Limits) error {
	if value == nil {
		return errors.New("plugin limits are required")
	}
	if value.GetMaxMessageBytes() == 0 || value.GetMaxTools() == 0 ||
		value.GetMaxSchemaBytes() == 0 || value.GetMaxCallArgumentBytes() == 0 ||
		value.GetMaxResultBytes() == 0 || value.GetMaxProgressBytes() == 0 ||
		value.GetMaxConcurrentCalls() == 0 {
		return errors.New("plugin limits must all be positive")
	}
	if value.GetMaxTools() > maximumToolCount {
		return fmt.Errorf("plugin tool limit exceeds %d", maximumToolCount)
	}
	if value.GetMaxConcurrentCalls() > maximumConcurrentCalls {
		return fmt.Errorf("plugin concurrent-call limit exceeds %d", maximumConcurrentCalls)
	}
	if value.GetMaxMessageBytes() > InitializeBootstrapMaximumBytes {
		return fmt.Errorf("plugin message limit exceeds %d", InitializeBootstrapMaximumBytes)
	}
	for _, bounded := range []struct {
		label string
		value uint64
	}{
		{"schema", value.GetMaxSchemaBytes()},
		{"call argument", value.GetMaxCallArgumentBytes()},
		{"result", value.GetMaxResultBytes()},
		{"progress", value.GetMaxProgressBytes()},
	} {
		if bounded.value > value.GetMaxMessageBytes() {
			return fmt.Errorf("plugin %s limit exceeds the message limit", bounded.label)
		}
	}
	if value.GetMaxSchemaBytes() > tool.MaximumPayloadBytes ||
		value.GetMaxCallArgumentBytes() > tool.MaximumPayloadBytes ||
		value.GetMaxResultBytes() > tool.MaximumPayloadBytes {
		return fmt.Errorf("plugin JSON limit exceeds %d", tool.MaximumPayloadBytes)
	}
	if value.GetMaxProgressBytes() > tool.MaximumProgressBytes {
		return fmt.Errorf("plugin progress limit exceeds %d", tool.MaximumProgressBytes)
	}
	return nil
}

// NegotiateLimits selects the lower valid bound for every resource.
func NegotiateLimits(requested, available *Limits) (*Limits, error) {
	if err := ValidateLimits(requested); err != nil {
		return nil, fmt.Errorf("requested plugin limits: %w", err)
	}
	if err := ValidateLimits(available); err != nil {
		return nil, errors.New("available plugin limits are invalid")
	}
	return &Limits{
		MaxMessageBytes:      min(requested.GetMaxMessageBytes(), available.GetMaxMessageBytes()),
		MaxTools:             min(requested.GetMaxTools(), available.GetMaxTools()),
		MaxSchemaBytes:       min(requested.GetMaxSchemaBytes(), available.GetMaxSchemaBytes()),
		MaxCallArgumentBytes: min(requested.GetMaxCallArgumentBytes(), available.GetMaxCallArgumentBytes()),
		MaxResultBytes:       min(requested.GetMaxResultBytes(), available.GetMaxResultBytes()),
		MaxProgressBytes:     min(requested.GetMaxProgressBytes(), available.GetMaxProgressBytes()),
		MaxConcurrentCalls:   min(requested.GetMaxConcurrentCalls(), available.GetMaxConcurrentCalls()),
	}, nil
}

// ValidateInitializeRequest rejects malformed pre-negotiation input.
func ValidateInitializeRequest(value *InitializeRequest) error {
	if value == nil {
		return errors.New("plugin initialize request is required")
	}
	if err := commonv1.ValidateProtocolRange(value.GetProtocol()); err != nil {
		return fmt.Errorf("plugin protocol range: %w", err)
	}
	if err := ValidateBuildIdentity(value.GetHost()); err != nil {
		return fmt.Errorf("plugin host build: %w", err)
	}
	if err := validateCapabilityRequest(
		value.GetSupportedCapabilities(),
		value.GetRequiredCapabilities(),
	); err != nil {
		return err
	}
	if err := ValidateLimits(value.GetRequestedLimits()); err != nil {
		return fmt.Errorf("plugin requested limits: %w", err)
	}
	if len(value.GetLaunchId()) != LaunchIDBytes {
		return fmt.Errorf("plugin launch identity must contain %d bytes", LaunchIDBytes)
	}
	if len(value.GetHandshakeChallenge()) != HandshakeChallengeBytes {
		return fmt.Errorf("plugin handshake challenge must contain %d bytes", HandshakeChallengeBytes)
	}
	return commonv1.ValidateEncodedSize(value, InitializeBootstrapMaximumBytes)
}

// ValidateInitializeResponseForRequest authenticates and validates the exact
// response to request using the caller-owned per-launch secret. Neither the
// secret nor untrusted field contents are included in returned validation
// errors.
func ValidateInitializeResponseForRequest(
	request *InitializeRequest,
	response *InitializeResponse,
	secret []byte,
) error {
	if err := ValidateInitializeRequest(request); err != nil {
		return err
	}
	if err := validateInitializeResponse(response); err != nil {
		return err
	}
	if !bytes.Equal(request.GetLaunchId(), response.GetLaunchId()) {
		return errors.New("plugin initialize launch identity does not match the request")
	}
	if !bytes.Equal(request.GetHandshakeChallenge(), response.GetHandshakeChallenge()) {
		return errors.New("plugin initialize challenge does not match the request")
	}
	expected, err := initializeProof(request, response, secret)
	if err != nil {
		return err
	}
	if !hmac.Equal(expected, response.GetHandshakeProof()) {
		return errors.New("plugin initialize handshake proof is invalid")
	}
	if statusErr := commonv1.AsError(response.GetStatus()); statusErr != nil {
		return statusErr
	}
	if !protocolSelectedFrom(request.GetProtocol(), response.GetProtocol()) {
		return errors.New("plugin selected protocol is outside the requested range")
	}
	if !capabilitiesSelectedFrom(
		request.GetSupportedCapabilities(),
		request.GetRequiredCapabilities(),
		response.GetCapabilities(),
	) {
		return errors.New("plugin selected capabilities do not satisfy the request")
	}
	if !limitsSelectedFrom(request.GetRequestedLimits(), response.GetLimits()) {
		return errors.New("plugin selected limits exceed the request")
	}
	_, err = DecodeManifest(response.GetManifest(), response.GetLimits())
	return err
}

// SignInitializeResponse returns a deep copy with an HMAC-SHA256 proof over
// the versioned canonical request and response transcript, including unknown
// fields at every known message boundary.
func SignInitializeResponse(
	request *InitializeRequest,
	response *InitializeResponse,
	secret []byte,
) (*InitializeResponse, error) {
	if request == nil || response == nil {
		return nil, errors.New("plugin initialize request and response are required")
	}
	result := clone(response)
	result.HandshakeProof = nil
	proof, err := initializeProof(request, result, secret)
	if err != nil {
		return nil, err
	}
	result.HandshakeProof = proof
	return result, nil
}

func validateInitializeResponse(value *InitializeResponse) error {
	if value == nil {
		return errors.New("plugin initialize response is required")
	}
	if err := commonv1.ValidateEncodedSize(value, InitializeBootstrapMaximumBytes); err != nil {
		return err
	}
	if err := commonv1.ValidateStatus(value.GetStatus()); err != nil {
		return fmt.Errorf("plugin initialize status: %w", err)
	}
	if err := ValidateBuildIdentity(value.GetPlugin()); err != nil {
		return fmt.Errorf("plugin build identity: %w", err)
	}
	if len(value.GetLaunchId()) != LaunchIDBytes ||
		len(value.GetHandshakeChallenge()) != HandshakeChallengeBytes ||
		len(value.GetHandshakeProof()) != HandshakeProofBytes {
		return errors.New("plugin initialize handshake fields have invalid sizes")
	}
	if statusErr := commonv1.AsError(value.GetStatus()); statusErr != nil {
		if initializeResponseHasSuccessFields(value) {
			return errors.New("failed plugin initialize response contains negotiated fields")
		}
		return commonv1.ValidateEncodedSize(value, InitializeBootstrapMaximumBytes)
	}
	if !supportedProtocol(value.GetProtocol()) {
		return errors.New("plugin selected protocol is unsupported")
	}
	if err := commonv1.ValidateCapabilities(value.GetCapabilities()); err != nil {
		return fmt.Errorf("plugin selected capabilities: %w", err)
	}
	if err := ValidateLimits(value.GetLimits()); err != nil {
		return fmt.Errorf("plugin selected limits: %w", err)
	}
	if len(value.GetSessionId()) != SessionIDBytes {
		return fmt.Errorf("plugin session identity must contain %d bytes", SessionIDBytes)
	}
	if _, err := DecodeManifest(value.GetManifest(), value.GetLimits()); err != nil {
		return err
	}
	return commonv1.ValidateEncodedSize(value, value.GetLimits().GetMaxMessageBytes())
}

func initializeResponseHasSuccessFields(value *InitializeResponse) bool {
	return value.GetProtocol() != nil || value.GetCapabilities() != nil || value.GetLimits() != nil ||
		value.GetManifest() != nil || len(value.GetSessionId()) != 0
}

func initializeProof(
	request *InitializeRequest,
	response *InitializeResponse,
	secret []byte,
) ([]byte, error) {
	if len(secret) != HandshakeSecretBytes {
		return nil, fmt.Errorf("plugin handshake secret must contain %d bytes", HandshakeSecretBytes)
	}
	if request == nil || response == nil {
		return nil, errors.New("plugin handshake transcript is required")
	}
	transcript, err := CanonicalInitializeTranscript(request, response)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(transcript)
	return mac.Sum(nil), nil
}

// Catalog is an immutable validated plugin identity and tool-definition set.
type Catalog struct {
	name        string
	version     string
	definitions []tool.Definition
}

// DecodeManifest validates and defensively converts a wire manifest.
func DecodeManifest(value *Manifest, limits *Limits) (Catalog, error) {
	if value == nil {
		return Catalog{}, errors.New("plugin manifest is required")
	}
	if err := ValidateLimits(limits); err != nil {
		return Catalog{}, err
	}
	if err := token("plugin manifest name", value.GetName(), maximumTokenBytes); err != nil {
		return Catalog{}, err
	}
	if err := token("plugin manifest version", value.GetVersion(), maximumTokenBytes); err != nil {
		return Catalog{}, err
	}
	if len(value.GetTools()) == 0 || len(value.GetTools()) > int(limits.GetMaxTools()) {
		return Catalog{}, errors.New("plugin manifest tool count is outside negotiated limits")
	}
	definitions := make([]tool.Definition, 0, len(value.GetTools()))
	previous := ""
	for index, wireDefinition := range value.GetTools() {
		definition, err := decodeToolDefinition(wireDefinition, limits)
		if err != nil {
			return Catalog{}, fmt.Errorf("plugin tool definition %d: %w", index, err)
		}
		if previous != "" && previous >= definition.Name() {
			return Catalog{}, errors.New("plugin tool definitions must be sorted and unique")
		}
		previous = definition.Name()
		definitions = append(definitions, definition)
	}
	if err := commonv1.ValidateEncodedSize(value, limits.GetMaxMessageBytes()); err != nil {
		return Catalog{}, err
	}
	return Catalog{name: value.GetName(), version: value.GetVersion(), definitions: definitions}, nil
}

// NewCatalog validates and snapshots ordinary kernel tool definitions.
func NewCatalog(name, version string, definitions []tool.Definition, limits *Limits) (Catalog, error) {
	wire := &Manifest{Name: name, Version: version, Tools: make([]*ToolDefinition, 0, len(definitions))}
	for _, definition := range definitions {
		encoded, err := EncodeToolDefinition(definition)
		if err != nil {
			return Catalog{}, err
		}
		wire.Tools = append(wire.Tools, encoded)
	}
	slices.SortFunc(wire.Tools, func(left, right *ToolDefinition) int {
		return strings.Compare(left.GetName(), right.GetName())
	})
	return DecodeManifest(wire, limits)
}

// Name returns the stable manifest name.
func (catalog Catalog) Name() string { return catalog.name }

// Version returns the stable manifest version.
func (catalog Catalog) Version() string { return catalog.version }

// Definitions returns deep defensive copies in canonical name order.
func (catalog Catalog) Definitions() []tool.Definition {
	result := make([]tool.Definition, len(catalog.definitions))
	for index := range catalog.definitions {
		result[index] = catalog.definitions[index].Clone()
	}
	return result
}

// Manifest returns a fresh mutable wire representation of the immutable
// catalog.
func (catalog Catalog) Manifest() (*Manifest, error) {
	if err := token("plugin manifest name", catalog.name, maximumTokenBytes); err != nil {
		return nil, err
	}
	if err := token("plugin manifest version", catalog.version, maximumTokenBytes); err != nil {
		return nil, err
	}
	if len(catalog.definitions) == 0 {
		return nil, errors.New("plugin catalog contains no tool definitions")
	}
	result := &Manifest{Name: catalog.name, Version: catalog.version}
	for _, definition := range catalog.definitions {
		wire, err := EncodeToolDefinition(definition)
		if err != nil {
			return nil, err
		}
		result.Tools = append(result.Tools, wire)
	}
	return result, nil
}

// EncodeToolDefinition converts one validated kernel definition to the wire.
func EncodeToolDefinition(value tool.Definition) (*ToolDefinition, error) {
	if err := value.Validate(); err != nil {
		return nil, fmt.Errorf("plugin tool definition: %w", err)
	}
	capabilities := make([]string, 0, len(value.Capabilities()))
	for _, capability := range value.Capabilities() {
		capabilities = append(capabilities, string(capability))
	}
	effect, err := encodeEffect(value.Effect())
	if err != nil {
		return nil, err
	}
	replay, err := encodeReplaySafety(value.ReplaySafety())
	if err != nil {
		return nil, err
	}
	return &ToolDefinition{
		Name:            value.Name(),
		Description:     value.Description(),
		InputSchemaJson: value.InputSchema(),
		Effect:          effect,
		ReplaySafety:    replay,
		Capabilities:    &commonv1.CapabilitySet{Names: capabilities},
	}, nil
}

func decodeToolDefinition(value *ToolDefinition, limits *Limits) (tool.Definition, error) {
	if value == nil {
		return tool.Definition{}, errors.New("plugin tool definition is required")
	}
	if err := token("plugin tool name", value.GetName(), 128); err != nil {
		return tool.Definition{}, err
	}
	if err := boundedText("plugin tool description", value.GetDescription(), maximumDescriptionBytes); err != nil {
		return tool.Definition{}, err
	}
	if err := boundedJSON("plugin tool schema", value.GetInputSchemaJson(), limits.GetMaxSchemaBytes()); err != nil {
		return tool.Definition{}, err
	}
	effect, err := decodeEffect(value.GetEffect())
	if err != nil {
		return tool.Definition{}, err
	}
	replay, err := decodeReplaySafety(value.GetReplaySafety())
	if err != nil {
		return tool.Definition{}, err
	}
	if capabilityErr := commonv1.ValidateCapabilities(value.GetCapabilities()); capabilityErr != nil {
		return tool.Definition{}, fmt.Errorf("plugin tool capabilities: %w", capabilityErr)
	}
	capabilities := make([]tool.Capability, 0, len(value.GetCapabilities().GetNames()))
	for _, name := range value.GetCapabilities().GetNames() {
		capability, decodeErr := decodeCapability(name)
		if decodeErr != nil {
			return tool.Definition{}, decodeErr
		}
		capabilities = append(capabilities, capability)
	}
	definition, err := tool.NewDefinition(
		value.GetName(),
		value.GetDescription(),
		json.RawMessage(value.GetInputSchemaJson()),
		effect,
		replay,
		capabilities...,
	)
	if err != nil {
		return tool.Definition{}, errors.New("plugin tool definition metadata is inconsistent")
	}
	return definition, nil
}

// DecodeExecuteRequest validates one call and returns an immutable kernel
// value. expectedSession must be the exact initialized session identity.
func DecodeExecuteRequest(value *ExecuteRequest, expectedSession []byte, limits *Limits) (tool.Call, error) {
	if value == nil {
		return tool.Call{}, errors.New("plugin execute request is required")
	}
	if err := validateSession(value.GetSessionId(), expectedSession); err != nil {
		return tool.Call{}, err
	}
	if err := ValidateLimits(limits); err != nil {
		return tool.Call{}, err
	}
	if err := token("plugin call identity", value.GetCallId(), 128); err != nil {
		return tool.Call{}, errors.New("plugin execute request contains an invalid call identity")
	}
	if err := token("plugin tool name", value.GetToolName(), 128); err != nil {
		return tool.Call{}, errors.New("plugin execute request contains an invalid tool name")
	}
	if uint64(len(value.GetArgumentsJson())) > limits.GetMaxCallArgumentBytes() {
		return tool.Call{}, errors.New("plugin call arguments exceed the negotiated limit")
	}
	call, err := tool.NewCall(tool.CallID(value.GetCallId()), value.GetToolName(), value.GetArgumentsJson())
	if err != nil {
		return tool.Call{}, errors.New("plugin execute request contains an invalid call")
	}
	if err := commonv1.ValidateEncodedSize(value, limits.GetMaxMessageBytes()); err != nil {
		return tool.Call{}, err
	}
	return call, nil
}

// StreamValidator validates one Execute stream in order and converts each
// frame to immutable kernel values.
type StreamValidator struct {
	callID   tool.CallID
	limits   *Limits
	next     uint64
	terminal bool
}

// NewStreamValidator snapshots the request correlation and negotiated limits.
func NewStreamValidator(request *ExecuteRequest, expectedSession []byte, limits *Limits) (*StreamValidator, error) {
	call, err := DecodeExecuteRequest(request, expectedSession, limits)
	if err != nil {
		return nil, err
	}
	return &StreamValidator{
		callID: call.ID(),
		limits: clone(limits),
		next:   1,
	}, nil
}

// FrameKind identifies one validated converted stream frame.
type FrameKind uint8

const (
	// FrameProgress is one non-terminal progress value.
	FrameProgress FrameKind = iota + 1
	// FrameResult is one terminal model-visible result.
	FrameResult
	// FrameFailure is one terminal infrastructure failure.
	FrameFailure
)

// Frame is an immutable converted Execute response.
type Frame struct {
	kind     FrameKind
	progress tool.Progress
	result   tool.Result
	failure  *tool.ExecutionError
}

// Kind returns the frame variant.
func (frame Frame) Kind() FrameKind { return frame.kind }

// Progress returns the progress value when this is a progress frame.
func (frame Frame) Progress() (tool.Progress, bool) {
	return frame.progress, frame.kind == FrameProgress
}

// Result returns a defensive terminal result when this is a result frame.
func (frame Frame) Result() (tool.Result, bool) {
	return frame.result.Clone(), frame.kind == FrameResult
}

// Failure returns the immutable terminal execution failure when present.
func (frame Frame) Failure() (*tool.ExecutionError, bool) {
	return frame.failure, frame.kind == FrameFailure
}

// Accept validates the next frame. Calls after a terminal frame fail closed.
func (validator *StreamValidator) Accept(value *ExecuteResponse) (Frame, error) {
	if validator == nil || validator.limits == nil {
		return Frame{}, errors.New("plugin stream validator is unavailable")
	}
	if validator.terminal {
		return Frame{}, errors.New("plugin execute stream contains a post-terminal frame")
	}
	if value == nil {
		return Frame{}, errors.New("plugin execute stream frame is required")
	}
	if err := commonv1.ValidateEncodedSize(value, validator.limits.GetMaxMessageBytes()); err != nil {
		return Frame{}, err
	}
	if value.GetCallId() != string(validator.callID) {
		return Frame{}, errors.New("plugin execute frame call identity does not match the request")
	}
	if value.GetSequence() != validator.next {
		return Frame{}, errors.New("plugin execute frame sequence is not contiguous")
	}
	frame, terminal, err := validator.decodeFrame(value)
	if err != nil {
		return Frame{}, err
	}
	validator.next++
	validator.terminal = terminal
	return frame, nil
}

// Finish requires exactly one accepted terminal result or failure.
func (validator *StreamValidator) Finish() error {
	if validator == nil {
		return errors.New("plugin stream validator is unavailable")
	}
	if !validator.terminal {
		return errors.New("plugin execute stream ended without a terminal frame")
	}
	return nil
}

func (validator *StreamValidator) decodeFrame(value *ExecuteResponse) (Frame, bool, error) {
	switch payload := value.GetFrame().(type) {
	case *ExecuteResponse_Progress:
		if payload == nil || payload.Progress == nil {
			return Frame{}, false, errors.New("plugin progress frame is empty")
		}
		if uint64(len(payload.Progress.GetMessage())) > validator.limits.GetMaxProgressBytes() {
			return Frame{}, false, errors.New("plugin progress exceeds the negotiated limit")
		}
		progress, err := tool.NewProgress(validator.callID, payload.Progress.GetMessage())
		if err != nil {
			return Frame{}, false, errors.New("plugin progress is invalid")
		}
		return Frame{kind: FrameProgress, progress: progress}, false, nil
	case *ExecuteResponse_Result:
		if payload == nil || payload.Result == nil {
			return Frame{}, false, errors.New("plugin result frame is empty")
		}
		if uint64(len(payload.Result.GetContentJson())) > validator.limits.GetMaxResultBytes() {
			return Frame{}, false, errors.New("plugin result exceeds the negotiated limit")
		}
		var (
			result tool.Result
			err    error
		)
		if payload.Result.GetProblem() == "" {
			result, err = tool.NewResult(validator.callID, payload.Result.GetContentJson())
		} else {
			if err = boundedText("plugin result problem", payload.Result.GetProblem(), maximumProblemBytes); err == nil {
				result, err = tool.NewErrorResult(
					validator.callID,
					payload.Result.GetContentJson(),
					payload.Result.GetProblem(),
				)
			}
		}
		if err != nil {
			return Frame{}, false, errors.New("plugin result is invalid")
		}
		return Frame{kind: FrameResult, result: result}, true, nil
	case *ExecuteResponse_Failure:
		if payload == nil {
			return Frame{}, false, errors.New("plugin execution failure frame is empty")
		}
		failure, err := decodeExecutionFailure(validator.callID, payload.Failure)
		if err != nil {
			return Frame{}, false, err
		}
		return Frame{kind: FrameFailure, failure: failure}, true, nil
	case nil:
		return Frame{}, false, errors.New("plugin execute frame payload is required")
	default:
		return Frame{}, false, errors.New("plugin execute frame payload is unsupported")
	}
}

func decodeExecutionFailure(callID tool.CallID, value *ExecutionFailure) (*tool.ExecutionError, error) {
	if value == nil {
		return nil, errors.New("plugin execution failure is empty")
	}
	state, err := decodeExecutionState(value.GetState())
	if err != nil {
		return nil, err
	}
	retry, err := decodeRetryDisposition(value.GetRetry())
	if err != nil {
		return nil, err
	}
	if textErr := boundedText("plugin execution failure message", value.GetSafeMessage(), tool.MaximumExecutionErrorBytes); textErr != nil {
		return nil, textErr
	}
	failure, err := tool.NewExecutionError(callID, state, retry, errors.New(value.GetSafeMessage()))
	if err != nil {
		return nil, errors.New("plugin execution failure metadata is inconsistent")
	}
	return failure, nil
}

// ValidateDrainRequest validates one initialized lifecycle request.
func ValidateDrainRequest(value *DrainRequest, expectedSession []byte, limits *Limits) error {
	if value == nil {
		return errors.New("plugin drain request is required")
	}
	if err := validateSession(value.GetSessionId(), expectedSession); err != nil {
		return err
	}
	return validateLifecycleSize(value, limits)
}

// ValidateDrainResponse rejects success while calls remain active.
func ValidateDrainResponse(value *DrainResponse, limits *Limits) error {
	if value == nil {
		return errors.New("plugin drain response is required")
	}
	if err := commonv1.ValidateStatus(value.GetStatus()); err != nil {
		return err
	}
	if value.GetStatus().GetCode() == commonv1.ErrorCode_ERROR_CODE_OK && value.GetActiveCalls() != 0 {
		return errors.New("successful plugin drain response contains active calls")
	}
	if limits != nil && value.GetActiveCalls() > limits.GetMaxConcurrentCalls() {
		return errors.New("plugin drain response exceeds the concurrent-call limit")
	}
	return validateLifecycleSize(value, limits)
}

// ValidateShutdownRequest validates one initialized lifecycle request.
func ValidateShutdownRequest(value *ShutdownRequest, expectedSession []byte, limits *Limits) error {
	if value == nil {
		return errors.New("plugin shutdown request is required")
	}
	if err := validateSession(value.GetSessionId(), expectedSession); err != nil {
		return err
	}
	return validateLifecycleSize(value, limits)
}

// ValidateShutdownResponse validates one bounded shutdown outcome.
func ValidateShutdownResponse(value *ShutdownResponse, limits *Limits) error {
	if value == nil {
		return errors.New("plugin shutdown response is required")
	}
	if err := commonv1.ValidateStatus(value.GetStatus()); err != nil {
		return err
	}
	return validateLifecycleSize(value, limits)
}

func validateLifecycleSize(value proto.Message, limits *Limits) error {
	if err := ValidateLimits(limits); err != nil {
		return err
	}
	return commonv1.ValidateEncodedSize(value, limits.GetMaxMessageBytes())
}

func validateSession(observed, expected []byte) error {
	if len(expected) != SessionIDBytes {
		return errors.New("expected plugin session identity is invalid")
	}
	if len(observed) != SessionIDBytes || !hmac.Equal(observed, expected) {
		return errors.New("plugin session identity does not match")
	}
	return nil
}

func validateCapabilityRequest(supported, required *commonv1.CapabilitySet) error {
	if err := commonv1.ValidateCapabilities(supported); err != nil {
		return fmt.Errorf("plugin supported capabilities: %w", err)
	}
	if err := commonv1.ValidateCapabilities(required); err != nil {
		return fmt.Errorf("plugin required capabilities: %w", err)
	}
	if !containsCapability(supported, CapabilityRuntimeToolsV1) ||
		!containsCapability(required, CapabilityRuntimeToolsV1) {
		return errors.New("plugin runtime-tools-v1 capability must be supported and required")
	}
	for _, name := range required.GetNames() {
		if _, found := slices.BinarySearch(supported.GetNames(), name); !found {
			return errors.New("plugin required capabilities must be supported by the host")
		}
	}
	return nil
}

func containsCapability(value *commonv1.CapabilitySet, name string) bool {
	if value == nil {
		return false
	}
	_, found := slices.BinarySearch(value.GetNames(), name)
	return found
}

func capabilitiesSelectedFrom(supported, required, selected *commonv1.CapabilitySet) bool {
	if commonv1.ValidateCapabilities(selected) != nil {
		return false
	}
	for _, name := range selected.GetNames() {
		if _, found := slices.BinarySearch(supported.GetNames(), name); !found {
			return false
		}
	}
	for _, name := range required.GetNames() {
		if _, found := slices.BinarySearch(selected.GetNames(), name); !found {
			return false
		}
	}
	return true
}

func limitsSelectedFrom(requested, selected *Limits) bool {
	if ValidateLimits(requested) != nil || ValidateLimits(selected) != nil {
		return false
	}
	return selected.GetMaxMessageBytes() <= requested.GetMaxMessageBytes() &&
		selected.GetMaxTools() <= requested.GetMaxTools() &&
		selected.GetMaxSchemaBytes() <= requested.GetMaxSchemaBytes() &&
		selected.GetMaxCallArgumentBytes() <= requested.GetMaxCallArgumentBytes() &&
		selected.GetMaxResultBytes() <= requested.GetMaxResultBytes() &&
		selected.GetMaxProgressBytes() <= requested.GetMaxProgressBytes() &&
		selected.GetMaxConcurrentCalls() <= requested.GetMaxConcurrentCalls()
}

func protocolSelectedFrom(requested *commonv1.ProtocolRange, selected *commonv1.ProtocolVersion) bool {
	if commonv1.ValidateProtocolRange(requested) != nil || selected == nil {
		return false
	}
	return compareProtocol(selected, requested.GetMinimum()) >= 0 &&
		compareProtocol(selected, requested.GetMaximum()) <= 0 && supportedProtocol(selected)
}

func supportedProtocol(value *commonv1.ProtocolVersion) bool {
	return value != nil && value.GetMajor() == ProtocolMajor &&
		value.GetMinor() >= ProtocolMinimumMinor && value.GetMinor() <= ProtocolMinor &&
		value.GetPatch() == ProtocolPatch
}

func compareProtocol(left, right *commonv1.ProtocolVersion) int {
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

func token(label, value string, maximum int) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be non-empty without surrounding whitespace", label)
	}
	if !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n\t") {
		return fmt.Errorf("%s contains invalid text", label)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	return nil
}

// ValidateBuildIdentity rejects incomplete, malformed, or unbounded runtime
// plugin build provenance without reflecting any field value in an error.
func ValidateBuildIdentity(value *BuildIdentity) error {
	if value == nil {
		return errors.New("plugin build identity is required")
	}
	for _, field := range []struct {
		label string
		value string
	}{
		{"component", value.GetComponent()},
		{"version", value.GetVersion()},
		{"commit", value.GetCommit()},
		{"runtime", value.GetRuntime()},
	} {
		if err := token("plugin build "+field.label, field.value, maximumTokenBytes); err != nil {
			return err
		}
	}
	return nil
}

func boundedText(label, value string, maximum int) error {
	return token(label, value, maximum)
}

func boundedJSON(label string, value []byte, maximum uint64) error {
	if len(value) == 0 || !json.Valid(value) {
		return fmt.Errorf("%s must be valid JSON", label)
	}
	if uint64(len(value)) > maximum {
		return fmt.Errorf("%s exceeds the negotiated limit", label)
	}
	return nil
}

func decodeCapability(value string) (tool.Capability, error) {
	capability := tool.Capability(value)
	switch capability {
	case tool.CapabilityFilesystemRead,
		tool.CapabilityFilesystemWrite,
		tool.CapabilityProcessExecute,
		tool.CapabilityNetworkAccess,
		tool.CapabilitySecretsRead,
		tool.CapabilityEnvironmentRead,
		tool.CapabilityEnvironmentWrite:
		return capability, nil
	default:
		return "", errors.New("plugin tool capability is unsupported")
	}
}

func encodeEffect(value tool.Effect) (ToolEffect, error) {
	switch value {
	case tool.EffectReadOnly:
		return ToolEffect_TOOL_EFFECT_READ_ONLY, nil
	case tool.EffectMutating:
		return ToolEffect_TOOL_EFFECT_MUTATING, nil
	default:
		return ToolEffect_TOOL_EFFECT_UNSPECIFIED, errors.New("plugin tool effect is unsupported")
	}
}

func decodeEffect(value ToolEffect) (tool.Effect, error) {
	switch value {
	case ToolEffect_TOOL_EFFECT_READ_ONLY:
		return tool.EffectReadOnly, nil
	case ToolEffect_TOOL_EFFECT_MUTATING:
		return tool.EffectMutating, nil
	default:
		return "", errors.New("plugin tool effect is unsupported")
	}
}

func encodeReplaySafety(value tool.ReplaySafety) (ReplaySafety, error) {
	switch value {
	case tool.ReplaySafe:
		return ReplaySafety_REPLAY_SAFETY_SAFE, nil
	case tool.ReplayIdempotent:
		return ReplaySafety_REPLAY_SAFETY_IDEMPOTENT, nil
	case tool.ReplayUnsafe:
		return ReplaySafety_REPLAY_SAFETY_UNSAFE, nil
	default:
		return ReplaySafety_REPLAY_SAFETY_UNSPECIFIED, errors.New("plugin tool replay safety is unsupported")
	}
}

func decodeReplaySafety(value ReplaySafety) (tool.ReplaySafety, error) {
	switch value {
	case ReplaySafety_REPLAY_SAFETY_SAFE:
		return tool.ReplaySafe, nil
	case ReplaySafety_REPLAY_SAFETY_IDEMPOTENT:
		return tool.ReplayIdempotent, nil
	case ReplaySafety_REPLAY_SAFETY_UNSAFE:
		return tool.ReplayUnsafe, nil
	default:
		return "", errors.New("plugin tool replay safety is unsupported")
	}
}

func decodeExecutionState(value ExecutionState) (tool.ExecutionState, error) {
	switch value {
	case ExecutionState_EXECUTION_STATE_DEFINITIVE:
		return tool.ExecutionDefinitive, nil
	case ExecutionState_EXECUTION_STATE_UNCERTAIN:
		return tool.ExecutionUncertain, nil
	default:
		return "", errors.New("plugin execution state is unsupported")
	}
}

func decodeRetryDisposition(value RetryDisposition) (tool.RetryDisposition, error) {
	switch value {
	case RetryDisposition_RETRY_DISPOSITION_NEVER:
		return tool.RetryNever, nil
	case RetryDisposition_RETRY_DISPOSITION_ALLOWED:
		return tool.RetryAllowed, nil
	default:
		return "", errors.New("plugin retry disposition is unsupported")
	}
}

func clone[T proto.Message](value T) T {
	result, ok := proto.Clone(value).(T)
	if !ok {
		panic("protobuf clone changed concrete plugin message type")
	}
	return result
}
