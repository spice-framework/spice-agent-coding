// Package grpcclient adapts the engine/v1 gRPC protocol to the transport-neutral
// client contracts. It authenticates every RPC, validates every protocol
// response before exposing it, and never retries mutations automatically.
//
// Protocol 1.3 initialization carries one caller-owned 128-bit attempt ID. The
// adapter may replay the exact request once when an Unavailable transport loses
// the response and validates the echoed identity. The immutable request retains
// that ID so callers can explicitly replay the exact intent if a response is
// lost. Every ambiguous transport outcome is an InitializationReplayError that
// carries the only safe attempt identity; cancellation and deadline causes are
// joined for errors.Is without permitting an automatic retry. Application,
// authentication, and invalid-request failures remain definitive.
//
// Legacy protocol 1.0-1.2 initialization has no replay identity. An ambiguous
// fresh allocation or reconnect ownership-CAS transport failure remains
// non-retryable and uncertain; callers must not manufacture a second intent.
package grpcclient
