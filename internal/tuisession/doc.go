// @import { Module } from "github.com/spice-framework/spice/annotation/modulith"

// Package tuisession adapts the transport-neutral agent client to the
// presentation-neutral TUI session contract.
//
// Construction is deliberately I/O-free. The first Receive or Perform call
// initializes the client session, and lifecycle cleanup owns every stream and
// the negotiated client session. Event replay advances only after a translated
// update has been accepted by the bounded presentation queue.
//
// @Module
package tuisession
