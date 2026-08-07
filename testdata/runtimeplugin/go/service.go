package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"slices"
	"sync"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	fixtureComponent = "spice-agent-distribution-fixture"
	fixtureVersion   = "v1"
	fixtureTool      = "fixture.echo"
)

var echoSchema = []byte(
	`{"type":"object","properties":{"value":{"type":"string"}},` +
		`"required":["value"],"additionalProperties":false}`,
)

type pluginService struct {
	pluginv1.UnimplementedPluginServiceServer

	mu          sync.Mutex
	secret      []byte
	limits      *pluginv1.Limits
	session     []byte
	initialized bool
	draining    bool
	closed      bool
	active      uint64
	zeroActive  chan struct{}
	shutdown    func()
}

func newPluginService(secret []byte, shutdown func()) (*pluginService, error) {
	if len(secret) != pluginv1.HandshakeSecretBytes || shutdown == nil {
		return nil, errors.New("construct plugin service")
	}
	zeroActive := make(chan struct{})
	close(zeroActive)
	return &pluginService{
		secret: slices.Clone(secret), limits: fixtureLimits(),
		zeroActive: zeroActive, shutdown: shutdown,
	}, nil
}

func (service *pluginService) Initialize(
	_ context.Context,
	request *pluginv1.InitializeRequest,
) (*pluginv1.InitializeResponse, error) {
	if err := pluginv1.ValidateInitializeRequest(request); err != nil {
		return nil, status.Error(codes.InvalidArgument, "plugin initialization request is invalid")
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return nil, status.Error(codes.FailedPrecondition, "plugin session is unavailable")
	}
	if service.initialized {
		return service.signInitialize(request, &pluginv1.InitializeResponse{
			Status: &commonv1.Status{
				Code:    commonv1.ErrorCode_ERROR_CODE_CONFLICT,
				Message: "plugin session is already initialized",
			},
			Plugin:             fixtureBuild(),
			LaunchId:           slices.Clone(request.GetLaunchId()),
			HandshakeChallenge: slices.Clone(request.GetHandshakeChallenge()),
		})
	}
	selectedProtocol, protocolStatus := commonv1.NegotiateProtocol(
		request.GetProtocol(),
		pluginv1.SupportedProtocolRange(),
	)
	if err := commonv1.AsError(protocolStatus); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "plugin protocol is incompatible")
	}
	limits, err := pluginv1.NegotiateLimits(request.GetRequestedLimits(), fixtureLimits())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "plugin limits are invalid")
	}
	manifest := fixtureManifest()
	if _, err = pluginv1.DecodeManifest(manifest, limits); err != nil {
		return nil, status.Error(codes.Internal, "plugin manifest is invalid")
	}
	session := make([]byte, pluginv1.SessionIDBytes)
	if _, err = io.ReadFull(rand.Reader, session); err != nil {
		clear(session)
		return nil, status.Error(codes.Internal, "plugin session creation failed")
	}
	response, err := service.signInitialize(request, &pluginv1.InitializeResponse{
		Status:   commonv1.OKStatus(),
		Protocol: selectedProtocol,
		Plugin:   fixtureBuild(),
		Capabilities: &commonv1.CapabilitySet{
			Names: []string{pluginv1.CapabilityRuntimeToolsV1},
		},
		Limits:             limits,
		Manifest:           manifest,
		LaunchId:           slices.Clone(request.GetLaunchId()),
		SessionId:          slices.Clone(session),
		HandshakeChallenge: slices.Clone(request.GetHandshakeChallenge()),
	})
	if err != nil {
		clear(session)
		return nil, status.Error(codes.Internal, "plugin handshake signing failed")
	}
	service.limits = limits
	service.session = session
	service.initialized = true
	return response, nil
}

func (service *pluginService) signInitialize(
	request *pluginv1.InitializeRequest,
	response *pluginv1.InitializeResponse,
) (*pluginv1.InitializeResponse, error) {
	return pluginv1.SignInitializeResponse(request, response, service.secret)
}

func (service *pluginService) Execute(
	request *pluginv1.ExecuteRequest,
	stream pluginv1.PluginService_ExecuteServer,
) error {
	service.mu.Lock()
	if !service.initialized || service.closed {
		service.mu.Unlock()
		return status.Error(codes.FailedPrecondition, "plugin session is unavailable")
	}
	if service.draining {
		service.mu.Unlock()
		return status.Error(codes.Unavailable, "plugin is draining")
	}
	limits := service.limits
	if uint64(len(request.GetArgumentsJson())) > limits.GetMaxCallArgumentBytes() {
		service.mu.Unlock()
		return status.Error(codes.ResourceExhausted, "plugin call exceeds the negotiated limit")
	}
	call, err := pluginv1.DecodeExecuteRequest(request, service.session, limits)
	if err != nil {
		service.mu.Unlock()
		return status.Error(codes.InvalidArgument, "plugin call is invalid")
	}
	if call.Name() != fixtureTool {
		service.mu.Unlock()
		return status.Error(codes.NotFound, "plugin tool is unavailable")
	}
	if service.active >= uint64(limits.GetMaxConcurrentCalls()) {
		service.mu.Unlock()
		return status.Error(codes.ResourceExhausted, "plugin call concurrency is exhausted")
	}
	if service.active == 0 {
		service.zeroActive = make(chan struct{})
	}
	service.active++
	service.mu.Unlock()
	defer service.finishCall()

	value, err := decodeEcho(call.Arguments())
	if err != nil {
		return status.Error(codes.InvalidArgument, "plugin echo arguments are invalid")
	}
	if err = stream.Send(&pluginv1.ExecuteResponse{
		CallId: string(call.ID()), Sequence: 1,
		Frame: &pluginv1.ExecuteResponse_Progress{
			Progress: &pluginv1.Progress{Message: "echo accepted"},
		},
	}); err != nil {
		return err
	}
	content, err := json.Marshal(struct {
		Value string `json:"value"`
	}{Value: value})
	if err != nil || uint64(len(content)) > limits.GetMaxResultBytes() {
		return status.Error(codes.ResourceExhausted, "plugin echo result exceeds the negotiated limit")
	}
	return stream.Send(&pluginv1.ExecuteResponse{
		CallId: string(call.ID()), Sequence: 2,
		Frame: &pluginv1.ExecuteResponse_Result{
			Result: &pluginv1.Result{ContentJson: content},
		},
	})
}

func (service *pluginService) finishCall() {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.active--
	if service.active == 0 {
		close(service.zeroActive)
	}
}

func (service *pluginService) clearSecrets() {
	service.mu.Lock()
	defer service.mu.Unlock()
	clear(service.secret)
	clear(service.session)
}

func (service *pluginService) Drain(
	ctx context.Context,
	request *pluginv1.DrainRequest,
) (*pluginv1.DrainResponse, error) {
	service.mu.Lock()
	if !service.initialized || service.closed {
		service.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "plugin session is unavailable")
	}
	if err := pluginv1.ValidateDrainRequest(request, service.session, service.limits); err != nil {
		service.mu.Unlock()
		return nil, status.Error(codes.InvalidArgument, "plugin session is invalid")
	}
	service.draining = true
	zeroActive := service.zeroActive
	limits := service.limits
	service.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	case <-zeroActive:
	}
	response := &pluginv1.DrainResponse{Status: commonv1.OKStatus()}
	if err := pluginv1.ValidateDrainResponse(response, limits); err != nil {
		return nil, status.Error(codes.Internal, "plugin drain response is invalid")
	}
	return response, nil
}

func (service *pluginService) Shutdown(
	_ context.Context,
	request *pluginv1.ShutdownRequest,
) (*pluginv1.ShutdownResponse, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if !service.initialized || service.closed {
		return nil, status.Error(codes.FailedPrecondition, "plugin session is unavailable")
	}
	if err := pluginv1.ValidateShutdownRequest(request, service.session, service.limits); err != nil {
		return nil, status.Error(codes.InvalidArgument, "plugin session is invalid")
	}
	if !service.draining || service.active != 0 {
		return nil, status.Error(codes.FailedPrecondition, "plugin must drain before shutdown")
	}
	response := &pluginv1.ShutdownResponse{Status: commonv1.OKStatus()}
	if err := pluginv1.ValidateShutdownResponse(response, service.limits); err != nil {
		return nil, status.Error(codes.Internal, "plugin shutdown response is invalid")
	}
	service.closed = true
	service.clearSecretsLocked()
	service.shutdown()
	return response, nil
}

func (service *pluginService) clearSecretsLocked() {
	clear(service.secret)
	clear(service.session)
}

func decodeEcho(arguments []byte) (string, error) {
	var input struct {
		Value string `json:"value"`
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Value == "" {
		return "", errors.New("decode echo arguments")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("decode trailing echo arguments")
	}
	return input.Value, nil
}

func fixtureLimits() *pluginv1.Limits {
	return &pluginv1.Limits{
		MaxMessageBytes: 64 << 10, MaxTools: 1, MaxSchemaBytes: 4 << 10,
		MaxCallArgumentBytes: 4 << 10, MaxResultBytes: 4 << 10,
		MaxProgressBytes: 256, MaxConcurrentCalls: 4,
	}
}

func fixtureBuild() *pluginv1.BuildIdentity {
	return &pluginv1.BuildIdentity{
		Component: fixtureComponent, Version: fixtureVersion,
		Commit: "fixture", Runtime: runtime.Version(),
	}
}

func fixtureManifest() *pluginv1.Manifest {
	return &pluginv1.Manifest{
		Name: fixtureComponent, Version: fixtureVersion,
		Tools: []*pluginv1.ToolDefinition{{
			Name: fixtureTool, Description: "Echo one bounded string value.",
			InputSchemaJson: slices.Clone(echoSchema),
			Effect:          pluginv1.ToolEffect_TOOL_EFFECT_READ_ONLY,
			ReplaySafety:    pluginv1.ReplaySafety_REPLAY_SAFETY_SAFE,
			Capabilities:    &commonv1.CapabilitySet{},
		}},
	}
}

var _ pluginv1.PluginServiceServer = (*pluginService)(nil)
