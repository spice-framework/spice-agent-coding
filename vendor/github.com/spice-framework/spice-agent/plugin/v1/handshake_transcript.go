package pluginv1

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

const (
	// InitializeTranscriptDomain identifies the only transcript authenticated
	// by the plugin/v1 initialization proof.
	InitializeTranscriptDomain = "spice-agent/plugin/v1/initialize"
	// InitializeTranscriptVersion selects the canonical JSON transcript shape.
	InitializeTranscriptVersion = uint32(1)
)

// CanonicalInitializeTranscript returns the normative, language-independent
// bytes authenticated by SignInitializeResponse. It uses fixed-position JSON
// arrays, exact JSON integers, unpadded base64url byte strings, and canonical
// unknown wire atoms in occurrence order. The response proof slot is always the empty
// byte string so the transcript does not authenticate itself.
//
// This function does not validate the negotiation. It rejects malformed
// unknown wire data, including protobuf groups, before a proof can be made.
func CanonicalInitializeTranscript(
	request *InitializeRequest,
	response *InitializeResponse,
) ([]byte, error) {
	if request == nil || response == nil {
		return nil, errors.New("plugin handshake transcript is required")
	}
	requestNode, err := canonicalInitializeRequest(request)
	if err != nil {
		return nil, fmt.Errorf("canonical plugin initialize request: %w", err)
	}
	responseNode, err := canonicalInitializeResponse(response)
	if err != nil {
		return nil, fmt.Errorf("canonical plugin initialize response: %w", err)
	}
	return marshalCanonicalJSON([]any{
		InitializeTranscriptDomain,
		InitializeTranscriptVersion,
		requestNode,
		responseNode,
	})
}

func canonicalInitializeRequest(value *InitializeRequest) ([]any, error) {
	protocol, err := canonicalProtocolRange(value.GetProtocol())
	if err != nil {
		return nil, err
	}
	host, err := canonicalBuildIdentity(value.GetHost())
	if err != nil {
		return nil, err
	}
	supported, err := canonicalCapabilitySet(value.GetSupportedCapabilities())
	if err != nil {
		return nil, err
	}
	required, err := canonicalCapabilitySet(value.GetRequiredCapabilities())
	if err != nil {
		return nil, err
	}
	limits, err := canonicalLimits(value.GetRequestedLimits())
	if err != nil {
		return nil, err
	}
	return canonicalMessage(
		value,
		"InitializeRequest",
		protocol,
		host,
		supported,
		required,
		limits,
		canonicalBytes(value.GetLaunchId()),
		canonicalBytes(value.GetHandshakeChallenge()),
	)
}

func canonicalInitializeResponse(value *InitializeResponse) ([]any, error) {
	status, err := canonicalStatus(value.GetStatus())
	if err != nil {
		return nil, err
	}
	protocol, err := canonicalProtocolVersion(value.GetProtocol())
	if err != nil {
		return nil, err
	}
	plugin, err := canonicalBuildIdentity(value.GetPlugin())
	if err != nil {
		return nil, err
	}
	capabilities, err := canonicalCapabilitySet(value.GetCapabilities())
	if err != nil {
		return nil, err
	}
	limits, err := canonicalLimits(value.GetLimits())
	if err != nil {
		return nil, err
	}
	manifest, err := canonicalManifest(value.GetManifest())
	if err != nil {
		return nil, err
	}
	return canonicalMessage(
		value,
		"InitializeResponse",
		status,
		protocol,
		plugin,
		capabilities,
		limits,
		manifest,
		canonicalBytes(value.GetLaunchId()),
		canonicalBytes(value.GetSessionId()),
		canonicalBytes(value.GetHandshakeChallenge()),
		canonicalBytes(nil),
	)
}

func canonicalProtocolVersion(value *commonv1.ProtocolVersion) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	return canonicalMessage(
		value,
		"ProtocolVersion",
		value.GetMajor(),
		value.GetMinor(),
		value.GetPatch(),
	)
}

func canonicalProtocolRange(value *commonv1.ProtocolRange) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	minimum, err := canonicalProtocolVersion(value.GetMinimum())
	if err != nil {
		return nil, err
	}
	maximum, err := canonicalProtocolVersion(value.GetMaximum())
	if err != nil {
		return nil, err
	}
	return canonicalMessage(value, "ProtocolRange", minimum, maximum)
}

func canonicalBuildIdentity(value *BuildIdentity) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	return canonicalMessage(
		value,
		"BuildIdentity",
		value.GetComponent(),
		value.GetVersion(),
		value.GetCommit(),
		value.GetRuntime(),
	)
}

func canonicalCapabilitySet(value *commonv1.CapabilitySet) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	names := make([]any, len(value.GetNames()))
	for index, name := range value.GetNames() {
		names[index] = name
	}
	return canonicalMessage(value, "CapabilitySet", names)
}

func canonicalLimits(value *Limits) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	return canonicalMessage(
		value,
		"Limits",
		value.GetMaxMessageBytes(),
		value.GetMaxTools(),
		value.GetMaxSchemaBytes(),
		value.GetMaxCallArgumentBytes(),
		value.GetMaxResultBytes(),
		value.GetMaxProgressBytes(),
		value.GetMaxConcurrentCalls(),
	)
}

func canonicalManifest(value *Manifest) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	tools := make([]any, len(value.GetTools()))
	for index, definition := range value.GetTools() {
		encoded, err := canonicalToolDefinition(definition)
		if err != nil {
			return nil, err
		}
		tools[index] = encoded
	}
	return canonicalMessage(value, "Manifest", value.GetName(), value.GetVersion(), tools)
}

func canonicalToolDefinition(value *ToolDefinition) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	capabilities, err := canonicalCapabilitySet(value.GetCapabilities())
	if err != nil {
		return nil, err
	}
	return canonicalMessage(
		value,
		"ToolDefinition",
		value.GetName(),
		value.GetDescription(),
		canonicalBytes(value.GetInputSchemaJson()),
		int32(value.GetEffect()),
		int32(value.GetReplaySafety()),
		capabilities,
	)
}

func canonicalStatus(value *commonv1.Status) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	detail, err := canonicalStatusDetail(value)
	if err != nil {
		return nil, err
	}
	return canonicalMessage(
		value,
		"Status",
		int32(value.GetCode()),
		value.GetMessage(),
		value.GetRetryable(),
		value.GetOperationId(),
		detail,
	)
}

func canonicalStatusDetail(value *commonv1.Status) ([]any, error) {
	switch detail := value.GetDetail().(type) {
	case nil:
		return nil, nil
	case *commonv1.Status_VersionMismatch:
		encoded, err := canonicalVersionMismatch(detail.VersionMismatch)
		return []any{"version_mismatch", encoded}, err
	case *commonv1.Status_CapabilityMismatch:
		encoded, err := canonicalCapabilityMismatch(detail.CapabilityMismatch)
		return []any{"capability_mismatch", encoded}, err
	case *commonv1.Status_ReplayBounds:
		encoded, err := canonicalReplayBounds(detail.ReplayBounds)
		return []any{"replay_bounds", encoded}, err
	case *commonv1.Status_Overload:
		encoded, err := canonicalOverload(detail.Overload)
		return []any{"overload", encoded}, err
	case *commonv1.Status_StaleClient:
		encoded, err := canonicalStaleClient(detail.StaleClient)
		return []any{"stale_client", encoded}, err
	case *commonv1.Status_SnapshotVersionMismatch:
		encoded, err := canonicalSnapshotVersionMismatch(detail.SnapshotVersionMismatch)
		return []any{"snapshot_version_mismatch", encoded}, err
	case *commonv1.Status_UncertainOperation:
		encoded, err := canonicalUncertainOperation(detail.UncertainOperation)
		return []any{"uncertain_operation", encoded}, err
	default:
		return nil, errors.New("plugin initialize status contains an unsupported known detail")
	}
}

func canonicalVersionMismatch(value *commonv1.VersionMismatch) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	client, err := canonicalProtocolRange(value.GetClient())
	if err != nil {
		return nil, err
	}
	server, err := canonicalProtocolRange(value.GetServer())
	if err != nil {
		return nil, err
	}
	return canonicalMessage(value, "VersionMismatch", client, server)
}

func canonicalCapabilityMismatch(value *commonv1.CapabilityMismatch) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	return canonicalMessage(
		value,
		"CapabilityMismatch",
		canonicalStrings(value.GetRequired()),
		canonicalStrings(value.GetAvailable()),
		canonicalStrings(value.GetMissing()),
	)
}

func canonicalReplayBounds(value *commonv1.ReplayBounds) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	return canonicalMessage(
		value,
		"ReplayBounds",
		value.GetRequestedAfterSequence(),
		value.GetEarliestSequence(),
		value.GetLatestSequence(),
		value.GetRecoverySequence(),
	)
}

func canonicalOverload(value *commonv1.Overload) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	return canonicalMessage(
		value,
		"Overload",
		value.GetResource(),
		value.GetLimit(),
		value.GetObserved(),
	)
}

func canonicalStaleClient(value *commonv1.StaleClient) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	return canonicalMessage(
		value,
		"StaleClient",
		value.GetExpectedEpoch(),
		value.GetObservedEpoch(),
	)
}

func canonicalSnapshotVersionMismatch(value *commonv1.SnapshotVersionMismatch) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	return canonicalMessage(
		value,
		"SnapshotVersionMismatch",
		value.GetExpected(),
		value.GetObserved(),
	)
}

func canonicalUncertainOperation(value *commonv1.UncertainOperation) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	return canonicalMessage(
		value,
		"UncertainOperation",
		value.GetOperationId(),
		value.GetOperationKind(),
	)
}

func canonicalStrings(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func canonicalMessage(value proto.Message, name string, fields ...any) ([]any, error) {
	unknown, err := canonicalUnknownFields(value)
	if err != nil {
		return nil, err
	}
	result := make([]any, 0, len(fields)+2)
	result = append(result, name)
	result = append(result, fields...)
	result = append(result, unknown)
	return result, nil
}

type unknownWireAtom struct {
	number      protowire.Number
	typeID      protowire.Type
	numberValue uint64
	bytesValue  []byte
}

func canonicalUnknownFields(value proto.Message) ([]any, error) {
	raw := value.ProtoReflect().GetUnknown()
	atoms := make([]unknownWireAtom, 0)
	for len(raw) > 0 {
		number, typeID, consumed := protowire.ConsumeTag(raw)
		if consumed < 0 || number <= 0 {
			return nil, errors.New("plugin handshake contains malformed unknown wire data")
		}
		raw = raw[consumed:]
		atom := unknownWireAtom{number: number, typeID: typeID}
		switch typeID {
		case protowire.VarintType:
			atom.numberValue, consumed = protowire.ConsumeVarint(raw)
		case protowire.Fixed64Type:
			atom.numberValue, consumed = protowire.ConsumeFixed64(raw)
		case protowire.BytesType:
			atom.bytesValue, consumed = protowire.ConsumeBytes(raw)
		case protowire.Fixed32Type:
			var fixed uint32
			fixed, consumed = protowire.ConsumeFixed32(raw)
			atom.numberValue = uint64(fixed)
		case protowire.StartGroupType, protowire.EndGroupType:
			return nil, errors.New("plugin handshake unknown protobuf groups are unsupported")
		default:
			return nil, errors.New("plugin handshake contains an unsupported unknown wire type")
		}
		if consumed < 0 {
			return nil, errors.New("plugin handshake contains malformed unknown wire data")
		}
		atoms = append(atoms, atom)
		raw = raw[consumed:]
	}
	result := make([]any, len(atoms))
	for index, atom := range atoms {
		var encoded any = atom.numberValue
		if atom.typeID == protowire.BytesType {
			encoded = canonicalBytes(atom.bytesValue)
		}
		result[index] = []any{atom.number, atom.typeID, encoded}
	}
	return result, nil
}

func canonicalBytes(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func marshalCanonicalJSON(value any) ([]byte, error) {
	if !canonicalStringsAreUTF8(value) {
		return nil, errors.New("canonical plugin handshake transcript contains invalid UTF-8")
	}
	var destination bytes.Buffer
	encoder := json.NewEncoder(&destination)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, errors.New("encode canonical plugin handshake transcript")
	}
	return bytes.TrimSuffix(destination.Bytes(), []byte{'\n'}), nil
}

func canonicalStringsAreUTF8(value any) bool {
	switch typed := value.(type) {
	case string:
		return utf8.ValidString(typed)
	case []any:
		for _, element := range typed {
			if !canonicalStringsAreUTF8(element) {
				return false
			}
		}
	}
	return true
}
