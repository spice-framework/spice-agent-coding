// Package agenttui defines bounded, immutable, UI-neutral terminal contracts
// for the standalone Spice Agent terminal product.
//
// The package owns semantic view data, prompt editing, key bindings, themes,
// deterministic rendering, UI-neutral Session updates, and shell lifecycle
// contracts. It deliberately does not define or discover a daemon, transport,
// coding-agent client, or executable entrypoint. Those boundaries are injected
// through generated application composition.
//
// @import { Module } from "github.com/spice-framework/spice/annotation/modulith"
// @Module
package agenttui
