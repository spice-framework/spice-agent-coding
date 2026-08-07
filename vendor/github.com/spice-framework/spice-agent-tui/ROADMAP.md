# Roadmap

Spice Agent has one authoritative sequence of implementation outcomes: the
[canonical implementation ledger](https://github.com/spice-framework/spice-agent/tree/main/docs/implementation).
This repository does not assign parallel phase numbers. It delivers the TUI
portion of that ledger while the owning repositories deliver the client,
protocol, daemon, and distribution contracts.

## Repository foundation

- [x] Establish Apache-2.0 licensing and governance.
- [x] Enforce Go 1.26.5 with cross-platform fast/check/verify gates.
- [x] Publish the bounded UI-neutral values needed by real presentation code.
- [x] Record explicit selected and unselected compatibility boundaries.
- [x] Pin the exact result-facts Spice core and toolchain revisions and publish the explicit-constructor TUI
      starter manifest without claiming shell auto-configuration.
- [x] Prove generic result facts through the real tool protocol with alias-positive
      and source-positioned negative compiler fixtures.
- [ ] Register the compatible revisions in the development catalog with the
      first generated application slice.

The checked items describe this repository's scaffold only. Its portion of the
canonical multi-repository foundation is not complete until the pending pin,
manifest, and catalog work above can be proved by executable product code.

## Adopted dependencies

- [ ] Adopt the separately reviewed high-level Spice Agent client/session API.
- [x] Define UI-neutral interaction and semantic-view values locally.
- [x] Define the UI-facing immutable Session snapshot/update and one-shot effect
      contracts, including cancellation, panic containment, revision ordering,
      and nested-intent rejection.
- [x] Add deterministic fake Session and lifecycle tests without inventing a
      transport client.
- [ ] Adopt client-owned reconnect, replay, backpressure, definition selection,
      daemon discovery, and transcript-redaction behavior.
- [ ] Keep the kernel, generated gRPC, daemon supervision, and OS IPC outside
      this module's dependency graph.

## Terminal product

- [x] Review and pin Bubble Tea v2.0.8 with the first production shell code.
- [x] Implement the deterministic shell/model skeleton, renderer, editor, and
      keybindings without faking application integration.
- [x] Publish `@UIShell` and `@UIRenderer` through a typed v1alpha2 Go tool;
      preserve generic compiler ownership of exact interface identity.
- [x] Adopt generic `Invocation.Facts` type-domain support from Spice/toolchain
      to enforce exact `Shell` and `Renderer` annotation results, including aliases.
- [x] Complete the independent presentation layer: accessible status semantics,
      grapheme-safe editing and cursor placement, deterministic resize,
      revisioned snapshot/activity updates, bounded prompt history, keyboard
      navigation, multi-size light/dark goldens, and clean Ctrl-C cancellation.
- [x] Prepare adapter-neutral Phase 4 contracts: injected ordered key bindings,
      semantic submit/cancel/respond/quit actions, bounded command and terminal
      values, command-owned effects with stale-token protection, caller-context
      cancellation, and line-oriented accessible rendering.
- [x] Add explicit shell auto-configuration with fallback renderer, Theme,
      ordered bindings, initial view, OS streams, accessibility config, and
      Shell; require an application-owned Session.
- [x] Expose public renderer/shell factories without Bubble Tea or internal
      presentation types.
- [x] Prove the public blank-import graph through committed generated Spice Go,
      freshness checks, external-package construction, startup, and shutdown.
- [ ] Adapt the bounded command contract to distribution commands after the
      client runner owns definition and endpoint selection.
- [ ] Translate the adopted client's reconnect, replay, backpressure, and error
      contracts into Session updates.
- [ ] Verify packaged Windows and Linux executables against a real daemon.
