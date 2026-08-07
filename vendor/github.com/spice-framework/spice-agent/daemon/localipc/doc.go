// Package localipc opens explicitly addressed, current-user-only local IPC
// connections. It performs no endpoint discovery, PATH lookup, network
// fallback, authentication, or daemon startup.
package localipc

import "errors"

var (
	// ErrUnsafeEndpoint reports an address whose type, ownership, permissions,
	// or platform-specific spelling is not safe for current-user IPC.
	ErrUnsafeEndpoint = errors.New("local IPC endpoint is unsafe")
	// ErrEndpointInUse reports an existing live Unix-domain listener detected
	// while safely distinguishing a stale socket. Listen never removes an
	// endpoint that responds to a connection probe.
	ErrEndpointInUse = errors.New("local IPC endpoint is already in use")
)
