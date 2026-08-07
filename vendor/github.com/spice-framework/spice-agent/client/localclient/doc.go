// Package localclient binds secure daemon endpoint discovery and local IPC to
// the transport-neutral client contracts. It never resolves network names,
// falls back to TCP, starts a process, or retries an RPC.
package localclient
