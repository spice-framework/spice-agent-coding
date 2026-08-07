// Package client defines the transport-neutral public contract used by Spice
// Agent clients and distribution adapters.
//
// The package deliberately contains no transport, daemon discovery, protocol,
// kernel, or presentation implementation. A Connector owns authentication and
// transport details outside these values, negotiates one Session, and translates
// its remote representation into the immutable types in this package.
//
// Session implementations must enforce stable-client ownership and must reject
// stale epochs. Event streams replay strictly after the supplied acknowledged
// Cursor and then remain sequence-contiguous. Interaction streams deliver one
// complete snapshot before revision-contiguous changes. Successful stream
// controls remain explicit frames rather than being inferred from end-of-stream.
// Callers own every context supplied to remote operations and decide when an
// event is durable enough to acknowledge. Close methods are local,
// context-free, nonblocking, and idempotent.
package client
