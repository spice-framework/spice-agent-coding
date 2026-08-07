package localclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"unicode"

	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/client/managed"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

// ErrExplicitEndpointMismatch reports that protected current-user metadata
// names a different endpoint than the address explicitly requested. Neither
// address is included because local paths may contain user information.
var ErrExplicitEndpointMismatch = errors.New("explicit local endpoint does not match protected metadata")

// ExplicitDiscovery resolves only one caller-selected address through the
// protected endpoint store. It never treats absence as startup authorization
// and never dials an address without matching active metadata and its token.
type ExplicitDiscovery struct {
	discovery *Discovery
}

// NewExplicitDiscovery binds one protected endpoint store and exact requested
// address without reading metadata or opening an IPC connection.
func NewExplicitDiscovery(store *endpoint.Store, requestedAddress string) (*ExplicitDiscovery, error) {
	if store == nil {
		return nil, errors.New("explicit local endpoint store is required")
	}
	return newExplicitDiscovery(store, requestedAddress)
}

func newExplicitDiscovery(
	source endpointDiscovery,
	requestedAddress string,
) (*ExplicitDiscovery, error) {
	if source == nil {
		return nil, errors.New("explicit local endpoint store is required")
	}
	if err := validateExplicitRequestedAddress(requestedAddress); err != nil {
		return nil, err
	}
	filtered := explicitEndpointSource{source: source, requestedAddress: requestedAddress}
	discovery, err := newDiscovery(filtered)
	if err != nil {
		return nil, err
	}
	return &ExplicitDiscovery{discovery: discovery}, nil
}

// Discover reads active protected metadata, requires an exact address match,
// and returns a lazy authenticated connector. Endpoint absence, stale cleanup,
// malformed metadata, and mismatch are hard attach failures; none can authorize
// managed startup.
func (discovery *ExplicitDiscovery) Discover(ctx context.Context) (client.Connector, error) {
	if discovery == nil || discovery.discovery == nil {
		return nil, errors.New("explicit local endpoint discovery is unavailable")
	}
	resolved, err := discovery.discovery.Discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("explicit local endpoint attach failed: %w", err)
	}
	return resolved, nil
}

// Close prevents future discovery and closes the cached lazy connector.
func (discovery *ExplicitDiscovery) Close() error {
	if discovery == nil || discovery.discovery == nil {
		return nil
	}
	return discovery.discovery.Close()
}

type explicitEndpointSource struct {
	source           endpointDiscovery
	requestedAddress string
}

func (source explicitEndpointSource) Discover(ctx context.Context) (endpoint.Metadata, error) {
	metadata, err := source.source.Discover(ctx)
	if cause := context.Cause(ctx); cause != nil {
		return endpoint.Metadata{}, cause
	}
	if err != nil {
		// Wrapping exact endpoint.ErrNotFound is intentional: explicit attach
		// must never produce the exact absence sentinel that authorizes startup.
		return endpoint.Metadata{}, fmt.Errorf("read protected explicit endpoint metadata: %w", err)
	}
	if metadata.Address() != source.requestedAddress {
		return endpoint.Metadata{}, ErrExplicitEndpointMismatch
	}
	return metadata, nil
}

func validateExplicitRequestedAddress(address string) error {
	if address == "" || len(address) > 1024 || address != strings.TrimSpace(address) ||
		strings.IndexFunc(address, unicode.IsControl) >= 0 {
		return errors.New("explicit local endpoint address is invalid")
	}
	return nil
}

func (*ExplicitDiscovery) String() string { return "localclient.ExplicitDiscovery([REDACTED])" }
func (discovery *ExplicitDiscovery) GoString() string {
	return discovery.String()
}

func (*ExplicitDiscovery) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "localclient.ExplicitDiscovery([REDACTED])")
}

func (*ExplicitDiscovery) MarshalJSON() ([]byte, error) {
	return json.Marshal("localclient.ExplicitDiscovery([REDACTED])")
}

func (discovery *ExplicitDiscovery) LogValue() slog.Value {
	return slog.StringValue(discovery.String())
}

var _ managed.Discovery = (*ExplicitDiscovery)(nil)
