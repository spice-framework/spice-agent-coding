// @import { Module } from "github.com/spice-framework/spice/annotation/modulith"

// Package daemoncommand owns the transport-neutral daemon command grammar.
// Transport construction is injected through Runner so the package cannot
// acquire a daemon, listener, or generated application itself.
//
// @Module
package daemoncommand
