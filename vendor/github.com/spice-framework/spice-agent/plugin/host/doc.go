// Package pluginhost owns the production boundary between a Spice Agent host
// and one configured runtime-plugin executable.
//
// Configuration is immutable after validation. Executables are selected by an
// absolute canonical path and an exact SHA-256 digest; no PATH lookup, runtime
// discovery, argument-based interpreter launch, or ambient environment
// inheritance occurs here. Process launch, protocol negotiation, and generation
// activation are layered on this foundation by separate components.
package pluginhost
