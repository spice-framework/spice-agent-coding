package grpcclient

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sync"

	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const authorizationMetadataKey = "authorization"

const maximumReconnectWaitersPerClient = 1

// Config contains the established gRPC channel and opaque local endpoint
// credential. Connector never owns or closes Connection. One Connector must
// exclusively manage each stable client identity whose reconnects it performs,
// so it can fence the prior local session before issuing the ownership CAS.
type Config struct {
	Connection grpc.ClientConnInterface
	Token      endpoint.Token
}

// Connector initializes authenticated transport-neutral client sessions.
type Connector struct {
	service enginev1.EngineServiceClient
	token   endpoint.Token

	mu         sync.Mutex
	sessions   map[string]*session
	reconnects map[string]*reconnectGate
}

type reconnectGate struct {
	token chan struct{}
	users int
}

// New constructs a connector over an established gRPC channel.
func New(config Config) (*Connector, error) {
	if config.Connection == nil {
		return nil, errors.New("gRPC client connection is required")
	}
	if err := config.Token.Validate(); err != nil {
		return nil, errors.New("gRPC endpoint credential is invalid")
	}
	return &Connector{
		service:    enginev1.NewEngineServiceClient(config.Connection),
		token:      config.Token,
		sessions:   make(map[string]*session),
		reconnects: make(map[string]*reconnectGate),
	}, nil
}

// Initialize negotiates a fresh or reconnecting stable-client ownership epoch.
// The caller's context owns only this negotiation, never the returned session.
func (connector *Connector) Initialize(
	ctx context.Context,
	request client.InitializeRequest,
) (client.Session, error) {
	if connector == nil || connector.service == nil || connector.token.Validate() != nil {
		return nil, unavailableError("gRPC connector is unavailable", false)
	}
	if ctx == nil {
		return nil, invalidArgumentError("initialize context is required")
	}
	if err := request.Validate(); err != nil {
		return nil, invalidArgumentError("initialize request is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, limitErr := limitsToWire(request.RequestedLimits()); limitErr != nil {
		return nil, invalidArgumentError("initialize requested limits exceed platform capacity")
	}
	wireRequest, err := initializeRequestToWire(request)
	if err != nil {
		return nil, protocolError()
	}
	reconnect, reconnecting := request.Reconnect()
	if reconnecting {
		release, acquireErr := connector.acquireReconnect(ctx, reconnect.ClientID())
		if acquireErr != nil {
			return nil, acquireErr
		}
		defer release()
		if err = connector.fenceReconnect(ctx, reconnect); err != nil {
			return nil, err
		}
	}
	receiveMaximum := max(
		uint64(enginev1.InitializeBootstrapMaximumBytes), request.RequestedLimits().MessageBytes(),
	)
	response, err := connector.initializeRPC(ctx, wireRequest, receiveMaximum)
	if err != nil {
		if attemptID, present := request.AttemptID(); present {
			return nil, initializationAttemptTransportError(ctx, err, attemptID)
		}
		if reconnecting {
			return nil, reconnectTransportError(ctx, err)
		}
		return nil, initializeTransportError(ctx, err)
	}
	if err = enginev1.ValidateInitializeResponseForRequest(wireRequest, response); err != nil {
		if statusErr := validatedStatusError(err); statusErr != nil {
			expected := statusContext{readOnly: true}
			if reconnecting {
				expected.sessionEpoch = reconnect.ExpectedEpoch()
			}
			return nil, statusToError(statusErr.Status(), expected)
		}
		return nil, protocolError()
	}
	connection, err := connectionFromWire(response)
	if err != nil {
		return nil, protocolError()
	}
	result := &session{
		service:    connector.service,
		token:      connector.token,
		connection: connection,
		streams:    make(map[*streamLifetime]struct{}),
		owner:      connector,
	}
	if !connector.install(result) {
		_ = result.Close()
		return nil, protocolError()
	}
	return result, nil
}

func (connector *Connector) initializeRPC(
	ctx context.Context,
	request *enginev1.InitializeRequest,
	receiveMaximum uint64,
) (*enginev1.InitializeResponse, error) {
	maximumAttempts := 1
	if len(request.GetInitializationAttemptId()) != 0 {
		maximumAttempts = 2
	}
	options := messageCallOptions(enginev1.InitializeBootstrapMaximumBytes, receiveMaximum)
	for attempt := range maximumAttempts {
		exact := proto.CloneOf(request)
		response, err := connector.service.Initialize(connector.authorized(ctx), exact, options...)
		if err == nil {
			return response, nil
		}
		if attempt+1 == maximumAttempts || !retryableInitializeTransport(ctx, err) {
			return nil, err
		}
	}
	return nil, errors.New("initialization retry exhausted without a transport result")
}

func retryableInitializeTransport(ctx context.Context, err error) bool {
	return ctx.Err() == nil && status.Code(err) == codes.Unavailable
}

func (connector *Connector) authorized(ctx context.Context) context.Context {
	return authorizedContext(ctx, connector.token)
}

func (connector *Connector) fenceReconnect(ctx context.Context, claim client.ReconnectClaim) error {
	connector.mu.Lock()
	prior := connector.sessions[claim.ClientID()]
	connector.mu.Unlock()
	if prior == nil || prior.connection.OwnershipEpoch() != claim.ExpectedEpoch() {
		return nil
	}
	return prior.fenceAndWait(ctx)
}

func (connector *Connector) install(current *session) bool {
	connector.mu.Lock()
	defer connector.mu.Unlock()
	if connector.sessions[current.connection.ClientID()] != nil {
		return false
	}
	if connector.sessions == nil {
		connector.sessions = make(map[string]*session)
	}
	connector.sessions[current.connection.ClientID()] = current
	return true
}

func (connector *Connector) acquireReconnect(ctx context.Context, clientID string) (func(), error) {
	connector.mu.Lock()
	if connector.reconnects == nil {
		connector.reconnects = make(map[string]*reconnectGate)
	}
	gate := connector.reconnects[clientID]
	if gate == nil {
		gate = &reconnectGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		connector.reconnects[clientID] = gate
	}
	if gate.users >= maximumReconnectWaitersPerClient+1 {
		connector.mu.Unlock()
		return nil, reconnectWaiterOverload()
	}
	gate.users++
	connector.mu.Unlock()

	select {
	case <-ctx.Done():
		connector.releaseReconnectReference(clientID, gate)
		return nil, ctx.Err()
	case <-gate.token:
	}
	if err := ctx.Err(); err != nil {
		gate.token <- struct{}{}
		connector.releaseReconnectReference(clientID, gate)
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			gate.token <- struct{}{}
			connector.releaseReconnectReference(clientID, gate)
		})
	}, nil
}

func (connector *Connector) releaseReconnectReference(clientID string, gate *reconnectGate) {
	connector.mu.Lock()
	gate.users--
	if gate.users == 0 && connector.reconnects[clientID] == gate {
		delete(connector.reconnects, clientID)
	}
	connector.mu.Unlock()
}

func (connector *Connector) remove(current *session) {
	connector.mu.Lock()
	if connector.sessions[current.connection.ClientID()] == current {
		delete(connector.sessions, current.connection.ClientID())
	}
	connector.mu.Unlock()
}

type session struct {
	service    enginev1.EngineServiceClient
	token      endpoint.Token
	connection client.Connection
	owner      *Connector

	mu      sync.Mutex
	closed  bool
	streams map[*streamLifetime]struct{}
}

func (current *session) Connection() client.Connection { return current.connection }

func (current *session) rpcContext(ctx context.Context) (context.Context, error) {
	if ctx == nil {
		return nil, invalidArgumentError("operation context is required")
	}
	current.mu.Lock()
	closed := current.closed
	current.mu.Unlock()
	if closed {
		return nil, client.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return authorizedContext(ctx, current.token), nil
}

func (current *session) callOptions() []grpc.CallOption {
	maximum := current.connection.Limits().MessageBytes()
	return messageCallOptions(maximum, maximum)
}

func (current *session) streamContext(ctx context.Context) (context.Context, *streamLifetime, error) {
	if ctx == nil {
		return nil, nil, invalidArgumentError("stream context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	lifetime, streamContext := newStreamLifetime(current)
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		lifetime.close()
		return nil, nil, client.ErrClosed
	}
	current.streams[lifetime] = struct{}{}
	current.mu.Unlock()
	return authorizedContext(streamContext, current.token), lifetime, nil
}

func (current *session) releaseStream(lifetime *streamLifetime) {
	current.mu.Lock()
	delete(current.streams, lifetime)
	remove := current.closed && len(current.streams) == 0
	current.mu.Unlock()
	if remove && current.owner != nil {
		current.owner.remove(current)
	}
}

func (current *session) Close() error {
	if current == nil {
		return nil
	}
	streams := current.closeAndCaptureStreams()
	for _, lifetime := range streams {
		lifetime.close()
	}
	return nil
}

func (current *session) closeAndCaptureStreams() []*streamLifetime {
	current.mu.Lock()
	current.closed = true
	streams := make([]*streamLifetime, 0, len(current.streams))
	for lifetime := range current.streams {
		streams = append(streams, lifetime)
	}
	remove := len(streams) == 0
	current.mu.Unlock()
	if remove && current.owner != nil {
		current.owner.remove(current)
	}
	return streams
}

func (current *session) fenceAndWait(ctx context.Context) error {
	if ctx == nil {
		return invalidArgumentError("reconnect context is required")
	}
	streams := current.closeAndCaptureStreams()
	for _, lifetime := range streams {
		lifetime.close()
	}
	for _, lifetime := range streams {
		if err := lifetime.waitFor(ctx); err != nil {
			return err
		}
	}
	return nil
}

func authorizedContext(ctx context.Context, token endpoint.Token) context.Context {
	authorization, _ := token.AuthorizationValue()
	values, _ := metadata.FromOutgoingContext(ctx)
	values = values.Copy()
	values.Delete(authorizationMetadataKey)
	values.Set(authorizationMetadataKey, authorization)
	return metadata.NewOutgoingContext(ctx, values)
}

// String returns non-secret connector state.
func (*Connector) String() string { return "grpcclient.Connector([REDACTED endpoint token])" }

// GoString returns non-secret connector state under Go-syntax formatting.
func (connector *Connector) GoString() string { return connector.String() }

// MarshalJSON prevents transport internals and credentials from being serialized.
func (*Connector) MarshalJSON() ([]byte, error) {
	return []byte(`"grpcclient.Connector([REDACTED endpoint token])"`), nil
}

// LogValue prevents transport internals and credentials from entering structured logs.
func (connector *Connector) LogValue() slog.Value { return slog.StringValue(connector.String()) }

func (*session) String() string           { return "grpcclient.Session([REDACTED endpoint token])" }
func (current *session) GoString() string { return current.String() }
func (*session) MarshalJSON() ([]byte, error) {
	return []byte(`"grpcclient.Session([REDACTED endpoint token])"`), nil
}
func (current *session) LogValue() slog.Value { return slog.StringValue(current.String()) }

func messageCallOptions(sendMaximum, receiveMaximum uint64) []grpc.CallOption {
	return []grpc.CallOption{
		// The adapter owns every permitted retry. Committing after the first
		// nonempty request message prevents caller-supplied service config from
		// multiplying initialization attempts or replaying mutations beneath us.
		grpc.MaxRetryRPCBufferSize(0),
		grpc.MaxCallSendMsgSize(platformMessageMaximum(sendMaximum)),
		grpc.MaxCallRecvMsgSize(platformMessageMaximum(receiveMaximum)),
	}
}

func platformMessageMaximum(value uint64) int {
	if value > uint64(math.MaxInt) {
		return math.MaxInt
	}
	return int(value) // #nosec G115 -- values above the platform maximum are clamped.
}

var (
	_ client.Connector = (*Connector)(nil)
	_ client.Session   = (*session)(nil)
)
