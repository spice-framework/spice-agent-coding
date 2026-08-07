package grpcclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/spice-framework/spice-agent/client"
	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
	"google.golang.org/protobuf/proto"
)

func initializeRequestToWire(request client.InitializeRequest) (*enginev1.InitializeRequest, error) {
	protocol, err := protocolRangeToWire(request.Protocol())
	if err != nil {
		return nil, err
	}
	build, err := buildToWire(request.Client())
	if err != nil {
		return nil, err
	}
	limits, err := limitsToWire(request.RequestedLimits())
	if err != nil {
		return nil, err
	}
	value := &enginev1.InitializeRequest{
		Protocol: protocol,
		Client:   build,
		SupportedCapabilities: &commonv1.CapabilitySet{
			Names: request.SupportedCapabilities(),
		},
		RequiredCapabilities: &commonv1.CapabilitySet{
			Names: request.RequiredCapabilities(),
		},
		RequestedLimits: limits,
	}
	if reconnect, ok := request.Reconnect(); ok {
		value.ReconnectClaim = &enginev1.ReconnectClaim{
			ClientId: reconnect.ClientID(), ExpectedOwnershipEpoch: reconnect.ExpectedEpoch(),
		}
	}
	if attempt, ok := request.AttemptID(); ok {
		encoded := attempt.Bytes()
		value.InitializationAttemptId = append([]byte(nil), encoded[:]...)
	}
	if err = enginev1.ValidateInitializeRequest(value); err != nil {
		return nil, err
	}
	return value, nil
}

func connectionFromWire(value *enginev1.InitializeResponse) (client.Connection, error) {
	protocol, err := protocolVersionFromWire(value.GetProtocol())
	if err != nil {
		return client.Connection{}, err
	}
	server, err := buildFromWire(value.GetServer())
	if err != nil {
		return client.Connection{}, err
	}
	limits, err := limitsFromWire(value.GetLimits())
	if err != nil {
		return client.Connection{}, err
	}
	health, err := healthFromWire(value.GetHealth())
	if err != nil {
		return client.Connection{}, err
	}
	catalog, err := catalogFromWire(value.GetDefinitions(), limits)
	if err != nil {
		return client.Connection{}, err
	}
	return client.NewConnection(client.ConnectionSpec{
		Protocol: protocol, Server: server, Capabilities: value.GetCapabilities().GetNames(),
		Limits: limits, Health: health, ClientID: value.GetClientId(),
		OwnershipEpoch: value.GetOwnershipEpoch(), Catalog: catalog,
	})
}

func protocolVersionToWire(value client.ProtocolVersion) (*commonv1.ProtocolVersion, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	result := &commonv1.ProtocolVersion{Major: value.Major(), Minor: value.Minor(), Patch: value.Patch()}
	if err := commonv1.ValidateProtocolRange(&commonv1.ProtocolRange{Minimum: result, Maximum: result}); err != nil {
		return nil, err
	}
	return result, nil
}

func protocolVersionFromWire(value *commonv1.ProtocolVersion) (client.ProtocolVersion, error) {
	if value == nil {
		return client.ProtocolVersion{}, errors.New("protocol version is required")
	}
	return client.NewProtocolVersion(value.GetMajor(), value.GetMinor(), value.GetPatch())
}

func protocolRangeToWire(value client.ProtocolRange) (*commonv1.ProtocolRange, error) {
	minimum, err := protocolVersionToWire(value.Minimum())
	if err != nil {
		return nil, err
	}
	maximum, err := protocolVersionToWire(value.Maximum())
	if err != nil {
		return nil, err
	}
	result := &commonv1.ProtocolRange{Minimum: minimum, Maximum: maximum}
	if err = commonv1.ValidateProtocolRange(result); err != nil {
		return nil, err
	}
	return result, nil
}

func protocolRangeFromWire(value *commonv1.ProtocolRange) (client.ProtocolRange, error) {
	if err := commonv1.ValidateProtocolRange(value); err != nil {
		return client.ProtocolRange{}, err
	}
	minimum, err := protocolVersionFromWire(value.GetMinimum())
	if err != nil {
		return client.ProtocolRange{}, err
	}
	maximum, err := protocolVersionFromWire(value.GetMaximum())
	if err != nil {
		return client.ProtocolRange{}, err
	}
	return client.NewProtocolRange(minimum, maximum)
}

func buildToWire(value client.Build) (*commonv1.BuildIdentity, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	result := &commonv1.BuildIdentity{
		Component: value.Component(), Version: value.Version(), Commit: value.Commit(), GoVersion: value.GoVersion(),
	}
	if err := commonv1.ValidateBuildIdentity(result); err != nil {
		return nil, err
	}
	return result, nil
}

func buildFromWire(value *commonv1.BuildIdentity) (client.Build, error) {
	if err := commonv1.ValidateBuildIdentity(value); err != nil {
		return client.Build{}, err
	}
	return client.NewBuild(value.GetComponent(), value.GetVersion(), value.GetCommit(), value.GetGoVersion())
}

func limitsToWire(value client.Limits) (*commonv1.Limits, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	result := &commonv1.Limits{
		MaxMessageBytes: value.MessageBytes(), MaxCollectionItems: value.CollectionItems(),
		MaxReplayEvents: value.ReplayEvents(), MaxReplayBytes: value.ReplayBytes(),
		MaxConcurrentStreams: value.ConcurrentStreams(), MaxActiveRuns: value.ActiveRuns(),
	}
	if err := commonv1.ValidateLimits(result); err != nil {
		return nil, err
	}
	if !limitsFitPlatform(result) {
		return nil, errors.New("client limits exceed platform capacity")
	}
	return result, nil
}

func limitsFromWire(value *commonv1.Limits) (client.Limits, error) {
	if err := commonv1.ValidateLimits(value); err != nil {
		return client.Limits{}, err
	}
	if !limitsFitPlatform(value) {
		return client.Limits{}, errors.New("protocol limits exceed client platform capacity")
	}
	return client.NewLimits(
		value.GetMaxMessageBytes(), value.GetMaxCollectionItems(), value.GetMaxReplayEvents(),
		value.GetMaxReplayBytes(), value.GetMaxConcurrentStreams(), value.GetMaxActiveRuns(),
	)
}

func limitsFitPlatform(value *commonv1.Limits) bool {
	if value == nil || value.GetMaxMessageBytes() > uint64(math.MaxInt) ||
		value.GetMaxReplayBytes() > uint64(math.MaxInt) {
		return false
	}
	for _, count := range []uint32{
		value.GetMaxCollectionItems(), value.GetMaxReplayEvents(),
		value.GetMaxConcurrentStreams(), value.GetMaxActiveRuns(),
	} {
		if uint64(count) > uint64(math.MaxInt) {
			return false
		}
	}
	return true
}

func healthFromWire(value *commonv1.Health) (client.Health, error) {
	if err := commonv1.ValidateHealth(value); err != nil {
		return client.Health{}, err
	}
	limits, err := limitsFromWire(value.GetLimits())
	if err != nil {
		return client.Health{}, err
	}
	var state client.HealthState
	switch value.GetState() {
	case commonv1.HealthState_HEALTH_STATE_STARTING:
		state = client.HealthStarting
	case commonv1.HealthState_HEALTH_STATE_READY:
		state = client.HealthReady
	case commonv1.HealthState_HEALTH_STATE_DEGRADED:
		state = client.HealthDegraded
	case commonv1.HealthState_HEALTH_STATE_STOPPING:
		state = client.HealthStopping
	default:
		return client.Health{}, errors.New("health state is unsupported")
	}
	return client.NewHealth(state, value.GetDegradedReasons(), value.GetActiveRuns(), limits)
}

func catalogFromWire(value *enginev1.DefinitionSet, limits client.Limits) (client.Catalog, error) {
	definitions := make([]client.Definition, 0, len(value.GetDefinitions()))
	for _, wireDefinition := range value.GetDefinitions() {
		reference, err := client.NewDefinitionRef(wireDefinition.GetId(), wireDefinition.GetRevision())
		if err != nil {
			return client.Catalog{}, err
		}
		definition, err := client.NewDefinition(reference, wireDefinition.GetModel(), wireDefinition.GetMaxTurns())
		if err != nil {
			return client.Catalog{}, err
		}
		definitions = append(definitions, definition)
	}
	return client.NewCatalog(value.GetRevision(), definitions, limits)
}

func snapshotToWire(value client.Snapshot) (*enginev1.SnapshotEnvelope, error) {
	encoded, err := value.MarshalBinary()
	if err != nil {
		return nil, err
	}
	result := new(enginev1.SnapshotEnvelope)
	if err = proto.Unmarshal(encoded, result); err != nil {
		return nil, errors.New("snapshot is not a Protobuf envelope")
	}
	if err = enginev1.ValidateSnapshotEnvelope(result); err != nil {
		return nil, err
	}
	return result, nil
}

func snapshotFromWire(value *enginev1.SnapshotEnvelope) (client.Snapshot, error) {
	if err := enginev1.ValidateSnapshotEnvelope(value); err != nil {
		return client.Snapshot{}, err
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(value)
	if err != nil {
		return client.Snapshot{}, err
	}
	return client.ParseSnapshot(encoded)
}

func pendingFromWire(value *enginev1.PendingInteraction) (client.PendingInteraction, error) {
	run, err := client.NewRunRef(value.GetRunId())
	if err != nil {
		return client.PendingInteraction{}, err
	}
	schema, err := client.ParseStructuredValue(value.GetSchemaJson())
	if err != nil {
		return client.PendingInteraction{}, err
	}
	return client.NewPendingInteraction(run, value.GetInteractionId(), value.GetKind(), value.GetPrompt(), schema)
}

func eventFromWire(value *enginev1.RunEvent) (client.Event, error) {
	if err := enginev1.ValidateRunEvent(value); err != nil {
		return client.Event{}, err
	}
	run, err := client.NewRunRef(value.GetRunId())
	if err != nil {
		return client.Event{}, err
	}
	kind, err := eventKindFromWire(value.GetKind())
	if err != nil {
		return client.Event{}, err
	}
	detail, err := eventDetailFromWire(kind, value.GetPayloadJson())
	if err != nil {
		return client.Event{}, err
	}
	return client.NewEvent(run, value.GetSequence(), time.Unix(0, value.GetUnixNano()).UTC(), kind, detail)
}

func eventKindFromWire(value enginev1.EventKind) (client.EventKind, error) {
	mapping := map[enginev1.EventKind]client.EventKind{
		enginev1.EventKind_EVENT_KIND_RUN_STARTED:           client.EventRunStarted,
		enginev1.EventKind_EVENT_KIND_RUN_COMPLETED:         client.EventRunCompleted,
		enginev1.EventKind_EVENT_KIND_RUN_FAILED:            client.EventRunFailed,
		enginev1.EventKind_EVENT_KIND_RUN_CANCELLED:         client.EventRunCancelled,
		enginev1.EventKind_EVENT_KIND_TURN_STARTED:          client.EventTurnStarted,
		enginev1.EventKind_EVENT_KIND_TURN_COMPLETED:        client.EventTurnCompleted,
		enginev1.EventKind_EVENT_KIND_TURN_FAILED:           client.EventTurnFailed,
		enginev1.EventKind_EVENT_KIND_MODEL_STARTED:         client.EventModelStarted,
		enginev1.EventKind_EVENT_KIND_MODEL_DELTA:           client.EventModelDelta,
		enginev1.EventKind_EVENT_KIND_MODEL_COMPLETED:       client.EventModelCompleted,
		enginev1.EventKind_EVENT_KIND_MODEL_FAILED:          client.EventModelFailed,
		enginev1.EventKind_EVENT_KIND_TOOL_STARTED:          client.EventToolStarted,
		enginev1.EventKind_EVENT_KIND_TOOL_PROGRESS:         client.EventToolProgress,
		enginev1.EventKind_EVENT_KIND_TOOL_COMPLETED:        client.EventToolCompleted,
		enginev1.EventKind_EVENT_KIND_TOOL_FAILED:           client.EventToolFailed,
		enginev1.EventKind_EVENT_KIND_INTERACTION_STARTED:   client.EventInteractionStarted,
		enginev1.EventKind_EVENT_KIND_INTERACTION_COMPLETED: client.EventInteractionCompleted,
		enginev1.EventKind_EVENT_KIND_INTERACTION_FAILED:    client.EventInteractionFailed,
		enginev1.EventKind_EVENT_KIND_INTERACTION_CANCELLED: client.EventInteractionCancelled,
	}
	result, ok := mapping[value]
	if !ok {
		return "", fmt.Errorf("event kind %d is unsupported", value)
	}
	return result, nil
}

func eventDetailFromWire(kind client.EventKind, encoded []byte) (client.EventDetail, error) {
	switch kind {
	case client.EventRunCompleted:
		if len(encoded) != 0 {
			return client.EventDetail{}, errors.New("completed run payload must be empty")
		}
		return client.NoEventDetail(), nil
	case client.EventRunStarted:
		var payload struct {
			Definition string `json:"definition"`
		}
		return decodeConstruct(encoded, &payload, func() (client.EventDetail, error) {
			return client.NewRunStartedDetail(payload.Definition)
		})
	case client.EventRunFailed, client.EventRunCancelled, client.EventTurnFailed:
		var payload struct {
			Error string `json:"error"`
		}
		return decodeConstruct(encoded, &payload, func() (client.EventDetail, error) {
			return client.NewStatusDetail(payload.Error)
		})
	case client.EventTurnStarted, client.EventTurnCompleted:
		var payload struct {
			Turn uint32 `json:"turn"`
		}
		return decodeConstruct(encoded, &payload, func() (client.EventDetail, error) {
			return client.NewTurnDetail(payload.Turn)
		})
	case client.EventModelStarted:
		var payload struct {
			Turn        uint32 `json:"turn"`
			OperationID string `json:"operation_id"`
		}
		return decodeConstruct(encoded, &payload, func() (client.EventDetail, error) {
			return client.NewModelStartedDetail(payload.Turn, payload.OperationID)
		})
	case client.EventModelDelta:
		var payload struct {
			Text string `json:"text"`
		}
		return decodeConstruct(encoded, &payload, func() (client.EventDetail, error) {
			return client.NewTextDetail(payload.Text)
		})
	case client.EventModelCompleted:
		var payload struct {
			InputTokens  uint64            `json:"input_tokens"`
			OutputTokens uint64            `json:"output_tokens"`
			Metadata     []json.RawMessage `json:"metadata,omitempty"`
		}
		return decodeConstruct(encoded, &payload, func() (client.EventDetail, error) {
			return client.NewModelCompletedDetail(client.NewUsage(payload.InputTokens, payload.OutputTokens)), nil
		})
	case client.EventModelFailed:
		var payload struct {
			Code         string            `json:"code"`
			Message      string            `json:"message"`
			Retryable    bool              `json:"retryable"`
			BeforeStream bool              `json:"before_stream"`
			Metadata     []json.RawMessage `json:"metadata,omitempty"`
		}
		return decodeConstruct(encoded, &payload, func() (client.EventDetail, error) {
			failure, err := client.NewModelFailure(payload.Code, payload.Message, payload.Retryable, payload.BeforeStream)
			if err != nil {
				return client.EventDetail{}, err
			}
			return client.NewModelFailedDetail(failure)
		})
	case client.EventToolStarted:
		var payload struct {
			CallID string `json:"call_id"`
			Name   string `json:"name"`
		}
		return decodeConstruct(encoded, &payload, func() (client.EventDetail, error) {
			return client.NewToolStartedDetail(payload.CallID, payload.Name)
		})
	case client.EventToolProgress:
		var payload struct {
			CallID  string `json:"call_id"`
			Message string `json:"message"`
		}
		return decodeConstruct(encoded, &payload, func() (client.EventDetail, error) {
			return client.NewToolProgressDetail(payload.CallID, payload.Message)
		})
	case client.EventToolCompleted, client.EventToolFailed:
		var payload struct {
			CallID  string `json:"call_id"`
			Name    string `json:"name"`
			Error   string `json:"error"`
			Outcome string `json:"outcome,omitempty"`
			Retry   string `json:"retry,omitempty"`
		}
		return decodeConstruct(encoded, &payload, func() (client.EventDetail, error) {
			terminal, err := client.NewToolTerminal(
				payload.CallID, payload.Name, payload.Error,
				client.ToolOutcome(payload.Outcome), client.ToolRetry(payload.Retry),
			)
			if err != nil {
				return client.EventDetail{}, err
			}
			return client.NewToolTerminalDetail(terminal)
		})
	case client.EventInteractionStarted:
		var payload struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		}
		return decodeConstruct(encoded, &payload, func() (client.EventDetail, error) {
			return client.NewInteractionStartedDetail(payload.ID, payload.Kind)
		})
	case client.EventInteractionCompleted, client.EventInteractionFailed, client.EventInteractionCancelled:
		var payload struct {
			ID    string `json:"id"`
			Error string `json:"error,omitempty"`
		}
		return decodeConstruct(encoded, &payload, func() (client.EventDetail, error) {
			return client.NewInteractionTerminalDetail(payload.ID, payload.Error)
		})
	default:
		return client.EventDetail{}, errors.New("event kind is unsupported")
	}
}

func decodeConstruct[T any](
	encoded []byte,
	payload *T,
	construct func() (client.EventDetail, error),
) (client.EventDetail, error) {
	if len(encoded) == 0 {
		return client.EventDetail{}, errors.New("event payload is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(payload); err != nil {
		return client.EventDetail{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return client.EventDetail{}, errors.New("event payload contains trailing JSON")
	}
	return construct()
}
