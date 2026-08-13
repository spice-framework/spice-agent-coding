# Roadmap

Spice Agent has one authoritative sequence of phase status, exact commits, and
acceptance evidence: the
[canonical implementation ledger](https://github.com/spice-framework/spice-agent/tree/main/docs/implementation).
This repository does not maintain a second mutable checklist.

## Repository ownership

`spice-agent-tui` owns only the terminal-facing extension surface:

- bounded UI-neutral presentation values and the `Session` boundary;
- `@UIShell` and `@UIRenderer` descriptors, their authorized annotation tool,
  and explicit Spice auto-configuration;
- the Bubble Tea shell, model, renderers, prompt editor, commands, key bindings,
  status presentation, themes, and accessibility behavior; and
- the deterministic `tuittest` harness for semantic interaction,
  cell-accurate captures, canonical strict-JSON double replay, committed
  lifecycle goldens, accessibility/contrast and complex-Unicode keyboard
  proofs, non-authoritative deterministic PNG review artifacts, and output-only
  VT conformance without a daemon or
  network; repository acceptance separately composes it with the exact test
  executable under a real Unix PTY or Windows ConPTY without adding process
  launch to the public API.

The kernel, public client transport, daemon supervision, Protobuf/gRPC
adapters, local IPC, provider and tool implementations, runtime-plugin host,
distribution commands, installers, and release orchestration remain in their
own repositories. Applications connect those concerns by injecting an
ordinary `Session`; this module does not acquire a registry or transport
container.

## Cross-repository status

Catalog registration, starter-manifest compatibility, generated distribution
composition, installed Windows/Linux terminal proof, preview publication, and
later contract stabilization are cross-repository outcomes. Their current
state and evidence live only in the canonical ledger and its phase documents:

- [Spice-native composition](https://github.com/spice-framework/spice-agent/blob/main/docs/implementation/02-spice-native-composition.md)
- [Daemon and TUI](https://github.com/spice-framework/spice-agent/blob/main/docs/implementation/05-daemon-and-tui.md)
- [Architecture-proof release](https://github.com/spice-framework/spice-agent/blob/main/docs/implementation/07-architecture-proof-release.md)
- [Stress prototypes and stabilization](https://github.com/spice-framework/spice-agent/blob/main/docs/implementation/08-stress-prototypes-and-stabilization.md)

Repository-local changes must preserve the generated Spice composition proof,
the UI-neutral `Session` boundary, deterministic rendering, cancellation, and
the public `tuittest` contract. Pre-1.0 APIs remain open to evidence-driven
revision until the canonical stabilization phase freezes compatibility.

The repository now carries one bounded Phase 7 evidence module:
[`experiments/semantic-shell`](experiments/semantic-shell) proves an alternate
semantic client against the published TUI module with no local replacement,
Bubble Tea import, terminal plugin, or transport ownership. This local evidence
now includes source-built Agent engine-protocol 1.2/1.3 peers over real local
IPC on Linux and Windows. It intentionally makes no released-binary N/N-1
claim and does not mark the cross-repository stabilization phase complete; the
canonical ledger remains authoritative.
