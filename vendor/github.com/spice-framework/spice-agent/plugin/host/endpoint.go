package pluginhost

import (
	"context"
	"net"
)

// LocalEndpoint is one caller-owned, current-user-only address reserved for a
// single runtime-plugin process. Dial must connect only to Address and must not
// consult proxies, service discovery, DNS, or ambient configuration. Close is
// called only after process containment has been proved; implementations may
// then remove a stale local endpoint artifact.
type LocalEndpoint interface {
	Address() string
	Dial(context.Context) (net.Conn, error)
	Close() error
}

// LocalEndpointFactory allocates one local address for a cryptographically
// random lowercase-hex launch identity. Open must not start a listener: the
// plugin process owns listening after it receives the address through private
// stdin. A non-nil endpoint returned alongside an error remains caller-owned.
type LocalEndpointFactory interface {
	Open(context.Context, string) (LocalEndpoint, error)
}
