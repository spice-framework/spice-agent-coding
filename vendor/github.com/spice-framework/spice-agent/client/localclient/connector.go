package localclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/client/grpcclient"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice-agent/daemon/localipc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const localTarget = "passthrough:///spice-agent-local"

type openedConnector struct {
	connector client.Connector
	close     func() error
}

type openConnector func(endpoint.Metadata) (openedConnector, error)

// Connector lazily owns one authenticated local gRPC channel and grpcclient
// adapter. Reusing that adapter across Initialize calls preserves reconnect
// fencing state until Connector closes or endpoint discovery replaces it.
type Connector struct {
	metadata endpoint.Metadata
	open     openConnector

	mu       sync.Mutex
	opened   *openedConnector
	sessions map[*ownedSession]struct{}
	closed   bool
	closeOne sync.Once
	closeErr error
}

// New constructs a lazy connector for one immutable endpoint description.
func New(metadata endpoint.Metadata) (*Connector, error) {
	return newConnector(metadata, openGRPCConnector)
}

func newConnector(metadata endpoint.Metadata, open openConnector) (*Connector, error) {
	if err := metadata.Validate(); err != nil {
		return nil, fmt.Errorf("local endpoint metadata is invalid: %w", err)
	}
	if err := validatePlatformTransport(metadata.Transport()); err != nil {
		return nil, err
	}
	if open == nil {
		return nil, errors.New("local connector opener is required")
	}
	return &Connector{metadata: metadata, open: open, sessions: make(map[*ownedSession]struct{})}, nil
}

// Initialize lazily dials the exact endpoint address and delegates one
// authenticated negotiation to the shared grpcclient adapter. It never retries
// a failed negotiation.
func (connector *Connector) Initialize(
	ctx context.Context,
	request client.InitializeRequest,
) (result client.Session, resultErr error) {
	if connector == nil || connector.open == nil {
		return nil, errors.New("local connector is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("local initialization context is required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("local initialization request is invalid: %w", err)
	}
	opened, err := connector.sharedConnector()
	if err != nil {
		return nil, fmt.Errorf("open local gRPC channel: %w", err)
	}
	session, err := opened.connector.Initialize(ctx, request)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("local gRPC connector returned a nil session")
	}
	if err = validateAdvertisedIdentity(connector.metadata, session.Connection()); err != nil {
		return nil, errors.Join(err, session.Close(), connector.Close())
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, errors.Join(cause, session.Close())
	}
	owned := &ownedSession{Session: session, owner: connector}
	connector.mu.Lock()
	if connector.closed || connector.opened != opened {
		connector.mu.Unlock()
		return nil, errors.Join(client.ErrClosed, owned.Close())
	}
	connector.sessions[owned] = struct{}{}
	connector.mu.Unlock()
	return owned, nil
}

func (connector *Connector) sharedConnector() (*openedConnector, error) {
	connector.mu.Lock()
	defer connector.mu.Unlock()
	if connector.closed {
		return nil, client.ErrClosed
	}
	if connector.opened != nil {
		return connector.opened, nil
	}
	opened, err := connector.open(connector.metadata)
	if err != nil {
		return nil, err
	}
	if opened.connector == nil || opened.close == nil {
		return nil, errors.Join(
			errors.New("local gRPC channel opener returned incomplete ownership"),
			closeOpened(opened),
		)
	}
	connector.opened = &opened
	return connector.opened, nil
}

func openGRPCConnector(metadata endpoint.Metadata) (openedConnector, error) {
	address := metadata.Address()
	connection, err := grpc.NewClient(
		localTarget,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return localipc.Dial(ctx, address)
		}),
		grpc.WithNoProxy(),
		grpc.WithDisableRetry(),
	)
	if err != nil {
		return openedConnector{}, err
	}
	adapter, err := grpcclient.New(grpcclient.Config{
		Connection: connection,
		Token:      metadata.Token(),
	})
	if err != nil {
		return openedConnector{}, errors.Join(err, connection.Close())
	}
	return openedConnector{connector: adapter, close: connection.Close}, nil
}

func closeOpened(opened openedConnector) error {
	if opened.close == nil {
		return nil
	}
	return opened.close()
}

func validateAdvertisedIdentity(metadata endpoint.Metadata, connection client.Connection) error {
	if !sameBuild(metadata.Server(), connection.Server()) {
		return errors.New("local daemon server identity differs from published endpoint metadata")
	}
	if !compatiblePublishedProtocol(metadata.Protocol(), connection.Protocol()) {
		return errors.New("local daemon protocol differs from published endpoint metadata")
	}
	return nil
}

func sameBuild(left, right client.Build) bool {
	return left.Component() == right.Component() && left.Version() == right.Version() &&
		left.Commit() == right.Commit() && left.GoVersion() == right.GoVersion()
}

func compatiblePublishedProtocol(published, selected client.ProtocolVersion) bool {
	if published.Major() != selected.Major() {
		return false
	}
	if selected.Minor() != published.Minor() {
		return selected.Minor() < published.Minor()
	}
	return selected.Patch() <= published.Patch()
}

type ownedSession struct {
	client.Session
	owner *Connector

	once sync.Once
	err  error
}

// Close fences this protocol session without closing the connector-owned shared
// channel. It is idempotent and concurrency-safe.
func (session *ownedSession) Close() error {
	if session == nil {
		return nil
	}
	session.once.Do(func() {
		var sessionErr error
		if session.Session != nil {
			sessionErr = session.Session.Close()
		}
		if session.owner != nil {
			session.owner.removeSession(session)
		}
		session.err = sessionErr
	})
	return session.err
}

func (connector *Connector) removeSession(session *ownedSession) {
	connector.mu.Lock()
	delete(connector.sessions, session)
	connector.mu.Unlock()
}

// Close fences every returned session and closes the one shared local gRPC
// channel. It is idempotent and safe to race with Initialize and Session.Close.
func (connector *Connector) Close() error {
	if connector == nil {
		return nil
	}
	connector.closeOne.Do(func() {
		connector.mu.Lock()
		connector.closed = true
		sessions := make([]*ownedSession, 0, len(connector.sessions))
		for session := range connector.sessions {
			sessions = append(sessions, session)
		}
		opened := connector.opened
		connector.opened = nil
		connector.mu.Unlock()
		var failures []error
		for _, session := range sessions {
			failures = append(failures, session.Close())
		}
		if opened != nil {
			failures = append(failures, closeOpened(*opened))
		}
		connector.closeErr = errors.Join(failures...)
	})
	return connector.closeErr
}

func (connector *Connector) available() bool {
	if connector == nil {
		return false
	}
	connector.mu.Lock()
	defer connector.mu.Unlock()
	return !connector.closed
}

// String prevents endpoint metadata and credentials from entering formatting.
func (*Connector) String() string { return "localclient.Connector([REDACTED])" }

// GoString prevents Go-syntax formatting from traversing private fields.
func (connector *Connector) GoString() string { return connector.String() }

// Format prevents every fmt verb from traversing private fields.
func (*Connector) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "localclient.Connector([REDACTED])")
}

// MarshalJSON makes accidental serialization visibly redacted.
func (*Connector) MarshalJSON() ([]byte, error) {
	return json.Marshal("localclient.Connector([REDACTED])")
}

// LogValue prevents endpoint material from entering structured logs.
func (connector *Connector) LogValue() slog.Value { return slog.StringValue(connector.String()) }

func (*ownedSession) String() string           { return "localclient.Session([REDACTED])" }
func (session *ownedSession) GoString() string { return session.String() }
func (*ownedSession) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "localclient.Session([REDACTED])")
}

func (*ownedSession) MarshalJSON() ([]byte, error) {
	return json.Marshal("localclient.Session([REDACTED])")
}
func (session *ownedSession) LogValue() slog.Value { return slog.StringValue(session.String()) }

var (
	_ client.Connector = (*Connector)(nil)
	_ client.Session   = (*ownedSession)(nil)
)
