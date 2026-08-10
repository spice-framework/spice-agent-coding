package daemon

import "net"

// ListenerFactory owns local transport creation. The interface keeps Runtime
// transport-agnostic and gives process-level acceptance tests a typed seam for
// faulting only an established client connection without replacing the daemon
// or bypassing its generated Spice graph.
type ListenerFactory interface {
	Listen(string) (net.Listener, error)
}
