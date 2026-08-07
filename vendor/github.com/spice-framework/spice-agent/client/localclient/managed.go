package localclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/client/managed"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

type endpointDiscovery interface {
	Discover(context.Context) (endpoint.Metadata, error)
}

// Discovery adapts secure endpoint.Store discovery to managed connector
// discovery. Only endpoint.ErrNotFound returned as the exact error authorizes
// managed daemon startup.
type Discovery struct {
	source endpointDiscovery

	operation sync.Mutex
	mu        sync.Mutex
	cached    *Connector
	metadata  endpoint.Metadata
	hasCached bool
	closed    bool
	closeOne  sync.Once
	closeErr  error
}

// NewDiscovery binds one endpoint store without performing discovery.
func NewDiscovery(store *endpoint.Store) (*Discovery, error) {
	if store == nil {
		return nil, errors.New("local endpoint discovery store is required")
	}
	return newDiscovery(store)
}

func newDiscovery(source endpointDiscovery) (*Discovery, error) {
	if source == nil {
		return nil, errors.New("local endpoint discovery store is required")
	}
	return &Discovery{source: source}, nil
}

// Discover resolves one secure endpoint into a lazy local connector.
func (discovery *Discovery) Discover(ctx context.Context) (client.Connector, error) {
	if discovery == nil || discovery.source == nil {
		return nil, errors.New("local endpoint discovery is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("local endpoint discovery context is required")
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	discovery.operation.Lock()
	defer discovery.operation.Unlock()
	discovery.mu.Lock()
	closed := discovery.closed
	discovery.mu.Unlock()
	if closed {
		return nil, errors.New("local endpoint discovery is closed")
	}
	metadata, err := discovery.source.Discover(ctx)
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	if err == endpoint.ErrNotFound { //nolint:errorlint // only this exact absence fact authorizes process startup.
		if closeErr := discovery.dropCached(); closeErr != nil {
			return nil, fmt.Errorf("close stale local endpoint connector: %w", closeErr)
		}
		return nil, managed.ErrEndpointNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("discover secure local endpoint: %w", err)
	}
	discovery.mu.Lock()
	if discovery.closed {
		discovery.mu.Unlock()
		return nil, errors.New("local endpoint discovery is closed")
	}
	if discovery.hasCached && sameEndpointMetadata(discovery.metadata, metadata) && discovery.cached.available() {
		cached := discovery.cached
		discovery.mu.Unlock()
		return cached, nil
	}
	discovery.mu.Unlock()
	connector, err := New(metadata)
	if err != nil {
		return nil, fmt.Errorf("construct local endpoint connector: %w", err)
	}
	discovery.mu.Lock()
	previous := discovery.cached
	discovery.cached = connector
	discovery.metadata = metadata
	discovery.hasCached = true
	discovery.mu.Unlock()
	if previous != nil {
		if closeErr := previous.Close(); closeErr != nil {
			return nil, fmt.Errorf("replace local endpoint connector: %w", closeErr)
		}
	}
	return connector, nil
}

func (discovery *Discovery) dropCached() error {
	discovery.mu.Lock()
	previous := discovery.cached
	discovery.cached = nil
	discovery.metadata = endpoint.Metadata{}
	discovery.hasCached = false
	discovery.mu.Unlock()
	if previous == nil {
		return nil
	}
	return previous.Close()
}

// Close prevents future discovery and closes the currently cached connector.
func (discovery *Discovery) Close() error {
	if discovery == nil {
		return nil
	}
	discovery.closeOne.Do(func() {
		discovery.operation.Lock()
		defer discovery.operation.Unlock()
		discovery.mu.Lock()
		discovery.closed = true
		discovery.mu.Unlock()
		discovery.closeErr = discovery.dropCached()
	})
	return discovery.closeErr
}

func sameEndpointMetadata(left, right endpoint.Metadata) bool {
	leftProcess, rightProcess := left.Process(), right.Process()
	return left.Transport() == right.Transport() && left.Address() == right.Address() &&
		left.Token().Equal(right.Token()) && sameBuild(left.Server(), right.Server()) &&
		sameExactProtocol(left.Protocol(), right.Protocol()) &&
		leftProcess.ID() == rightProcess.ID() && leftProcess.StartedAt().Equal(rightProcess.StartedAt()) &&
		bytes.Equal(leftProcess.InstanceID(), rightProcess.InstanceID())
}

func sameExactProtocol(left, right client.ProtocolVersion) bool {
	return left.Major() == right.Major() && left.Minor() == right.Minor() && left.Patch() == right.Patch()
}

type endpointStartup interface {
	AcquireStartup(context.Context) (*endpoint.StartupLease, error)
}

// StartupLock adapts endpoint.Store startup serialization to managed startup.
type StartupLock struct {
	source endpointStartup
}

// NewStartupLock binds one endpoint store without acquiring its startup lock.
func NewStartupLock(store *endpoint.Store) (*StartupLock, error) {
	if store == nil {
		return nil, errors.New("local endpoint startup store is required")
	}
	return newStartupLock(store)
}

func newStartupLock(source endpointStartup) (*StartupLock, error) {
	if source == nil {
		return nil, errors.New("local endpoint startup store is required")
	}
	return &StartupLock{source: source}, nil
}

// Acquire obtains one context-bounded, idempotently releasable startup lease.
func (lock *StartupLock) Acquire(ctx context.Context) (managed.StartupLease, error) {
	if lock == nil || lock.source == nil {
		return nil, errors.New("local endpoint startup lock is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("local endpoint startup context is required")
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	lease, err := lock.source.AcquireStartup(ctx)
	if cause := context.Cause(ctx); cause != nil {
		if lease != nil {
			return nil, errors.Join(cause, lease.Release())
		}
		return nil, cause
	}
	if err != nil {
		return lease, fmt.Errorf("acquire secure local endpoint startup lock: %w", err)
	}
	if lease == nil {
		return nil, errors.New("local endpoint store returned a nil startup lease")
	}
	return lease, nil
}

func (*Discovery) String() string             { return "localclient.Discovery([REDACTED])" }
func (discovery *Discovery) GoString() string { return discovery.String() }
func (*Discovery) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "localclient.Discovery([REDACTED])")
}

func (*Discovery) MarshalJSON() ([]byte, error) {
	return json.Marshal("localclient.Discovery([REDACTED])")
}
func (discovery *Discovery) LogValue() slog.Value { return slog.StringValue(discovery.String()) }

func (*StartupLock) String() string        { return "localclient.StartupLock([REDACTED])" }
func (lock *StartupLock) GoString() string { return lock.String() }
func (*StartupLock) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "localclient.StartupLock([REDACTED])")
}

func (*StartupLock) MarshalJSON() ([]byte, error) {
	return json.Marshal("localclient.StartupLock([REDACTED])")
}
func (lock *StartupLock) LogValue() slog.Value { return slog.StringValue(lock.String()) }

var (
	_ managed.Discovery   = (*Discovery)(nil)
	_ managed.StartupLock = (*StartupLock)(nil)
)
