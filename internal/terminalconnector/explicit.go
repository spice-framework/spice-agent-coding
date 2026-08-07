package terminalconnector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/client/localclient"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

// Explicit owns protected discovery for one caller-selected local endpoint.
// It never starts a daemon and never dials until Initialize.
type Explicit struct {
	discovery explicitDiscovery

	mu       sync.Mutex
	closed   bool
	closeOne sync.Once
	closeErr error
}

type explicitDiscovery interface {
	Discover(context.Context) (client.Connector, error)
	Close() error
}

// NewExplicit constructs an I/O-lazy explicit connector.
func NewExplicit(store *endpoint.Store, address string) (*Explicit, error) {
	discovery, err := localclient.NewExplicitDiscovery(store, address)
	if err != nil {
		return nil, fmt.Errorf("construct explicit endpoint discovery: %w", err)
	}
	return &Explicit{discovery: discovery}, nil
}

// Initialize discovers exact protected metadata, then negotiates one session.
func (connector *Explicit) Initialize(
	ctx context.Context,
	request client.InitializeRequest,
) (client.Session, error) {
	if connector == nil || connector.discovery == nil {
		return nil, errors.New("explicit terminal connector is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("explicit terminal initialization context is required")
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("explicit terminal initialization request: %w", err)
	}
	connector.mu.Lock()
	closed := connector.closed
	connector.mu.Unlock()
	if closed {
		return nil, client.ErrClosed
	}
	resolved, err := connector.discovery.Discover(ctx)
	if err != nil {
		return nil, &opaqueError{message: "discover explicit local endpoint", cause: err}
	}
	if resolved == nil {
		return nil, errors.New("explicit endpoint discovery returned no connector")
	}
	session, err := resolved.Initialize(ctx, request)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("explicit local endpoint returned no session")
	}
	connector.mu.Lock()
	closed = connector.closed
	connector.mu.Unlock()
	if closed {
		return nil, errors.Join(client.ErrClosed, session.Close())
	}
	return session, nil
}

// Close fences discovery and its cached local gRPC connector.
func (connector *Explicit) Close() error {
	if connector == nil {
		return nil
	}
	connector.closeOne.Do(func() {
		connector.mu.Lock()
		connector.closed = true
		connector.mu.Unlock()
		if connector.discovery != nil {
			connector.closeErr = connector.discovery.Close()
		}
	})
	return connector.closeErr
}

func (*Explicit) String() string             { return "terminalconnector.Explicit([REDACTED])" }
func (connector *Explicit) GoString() string { return connector.String() }
func (*Explicit) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "terminalconnector.Explicit([REDACTED])") //nolint:errcheck // fmt.Formatter cannot return an error.
}

func (*Explicit) MarshalJSON() ([]byte, error) {
	return json.Marshal("terminalconnector.Explicit([REDACTED])")
}
func (connector *Explicit) LogValue() slog.Value { return slog.StringValue(connector.String()) }

type opaqueError struct {
	message string
	cause   error
}

func (failure *opaqueError) Error() string { return failure.message }
func (failure *opaqueError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

var _ client.Connector = (*Explicit)(nil)
