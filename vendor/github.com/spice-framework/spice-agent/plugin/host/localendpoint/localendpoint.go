// Package localendpoint allocates caller-owned, current-user-only runtime
// plugin endpoints without listening, discovery, DNS, or network fallback.
package localendpoint

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice-agent/daemon/localipc"
	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
)

var (
	// ErrInvalidLaunchIdentity reports a launch identity that is not the exact
	// lowercase-hex spelling of a nonzero plugin/v1 launch identity.
	ErrInvalidLaunchIdentity = errors.New("runtime plugin launch identity is invalid")
	// ErrClosed reports use of an endpoint after its ownership was released.
	ErrClosed = errors.New("runtime plugin endpoint is closed")
	// ErrUnavailable reports failure to allocate or connect to a local endpoint.
	ErrUnavailable = errors.New("runtime plugin endpoint is unavailable")
	// ErrCleanup reports failure to safely remove an owned stale endpoint artifact.
	ErrCleanup = errors.New("runtime plugin endpoint cleanup failed")
)

const redactedEndpoint = "localendpoint.Endpoint([REDACTED])"

// Factory allocates local runtime-plugin addresses in the current user's
// operating-system-protected endpoint scope. Its zero value is ready for use.
type Factory struct{}

// NewFactory returns a stateless current-user endpoint factory.
func NewFactory() *Factory { return &Factory{} }

// Open derives but does not listen on the endpoint for launchIdentity.
func (*Factory) Open(ctx context.Context, launchIdentity string) (pluginhost.LocalEndpoint, error) {
	if ctx == nil {
		return nil, errors.New("runtime plugin endpoint context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := parseLaunchIdentity(launchIdentity)
	if err != nil {
		return nil, err
	}
	scope, err := endpoint.CurrentUserScope()
	if err != nil {
		return nil, fmt.Errorf("open current-user runtime plugin endpoint: %w", ErrUnavailable)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	address, err := derivePlatformAddress(scope, identity)
	if err != nil {
		return nil, err
	}
	return &ownedEndpoint{address: address}, nil
}

type launchIdentity [pluginv1.LaunchIDBytes]byte

func parseLaunchIdentity(encoded string) (launchIdentity, error) {
	var identity launchIdentity
	if len(encoded) != hex.EncodedLen(len(identity)) {
		return launchIdentity{}, ErrInvalidLaunchIdentity
	}
	for _, current := range encoded {
		if current < '0' || current > '9' && current < 'a' || current > 'f' {
			return launchIdentity{}, ErrInvalidLaunchIdentity
		}
	}
	if _, err := hex.Decode(identity[:], []byte(encoded)); err != nil {
		return launchIdentity{}, ErrInvalidLaunchIdentity
	}
	var combined byte
	for _, current := range identity {
		combined |= current
	}
	if combined == 0 {
		return launchIdentity{}, ErrInvalidLaunchIdentity
	}
	return identity, nil
}

type ownedEndpoint struct {
	address string
	closed  atomic.Bool

	closeOnce sync.Once
	closeErr  error
}

func (owned *ownedEndpoint) Address() string {
	if owned == nil {
		return ""
	}
	return owned.address
}

func (owned *ownedEndpoint) Dial(ctx context.Context) (net.Conn, error) {
	if ctx == nil {
		return nil, errors.New("runtime plugin endpoint dial context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if owned == nil || owned.closed.Load() {
		return nil, ErrClosed
	}
	connection, err := localipc.Dial(ctx, owned.address)
	if err != nil {
		return nil, safeOperationError("dial runtime plugin endpoint", ErrUnavailable, err)
	}
	if owned.closed.Load() {
		_ = connection.Close()
		return nil, ErrClosed
	}
	return connection, nil
}

func (owned *ownedEndpoint) Close() error {
	if owned == nil {
		return nil
	}
	owned.closeOnce.Do(func() {
		owned.closed.Store(true)
		if err := cleanupPlatformAddress(owned.address); err != nil {
			owned.closeErr = safeOperationError("clean runtime plugin endpoint", ErrCleanup, err)
		}
	})
	return owned.closeErr
}

func (*ownedEndpoint) String() string   { return redactedEndpoint }
func (*ownedEndpoint) GoString() string { return redactedEndpoint }
func (*ownedEndpoint) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, redactedEndpoint)
}
func (*ownedEndpoint) MarshalJSON() ([]byte, error) { return json.Marshal(redactedEndpoint) }

func safeOperationError(operation string, primary, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	var safeCause error
	switch {
	case errors.Is(err, localipc.ErrUnsafeEndpoint):
		safeCause = localipc.ErrUnsafeEndpoint
	case errors.Is(err, localipc.ErrEndpointInUse):
		safeCause = localipc.ErrEndpointInUse
	}
	if safeCause != nil {
		return fmt.Errorf("%s: %w", operation, errors.Join(primary, safeCause))
	}
	return fmt.Errorf("%s: %w", operation, primary)
}

var (
	_ pluginhost.LocalEndpointFactory = (*Factory)(nil)
	_ pluginhost.LocalEndpoint        = (*ownedEndpoint)(nil)
	_ fmt.Stringer                    = (*ownedEndpoint)(nil)
	_ fmt.GoStringer                  = (*ownedEndpoint)(nil)
	_ json.Marshaler                  = (*ownedEndpoint)(nil)
)
