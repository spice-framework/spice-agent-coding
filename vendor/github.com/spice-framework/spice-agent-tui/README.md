# Spice Agent TUI

Unified documentation: [spiceframework.dev/agent/interfaces/tui](https://spiceframework.dev/agent/interfaces/tui/).

This repository owns the terminal experience for Spice Agent. Its public API is
UI-neutral; Bubble Tea v2 is confined to `internal/presentation` and is reached
through the `terminal` facade.

The implemented slice includes:

- bounded immutable text, workspace, status, prompt, theme, frame, command,
  intent, session snapshot, and tagged session-update values;
- a `Session` seam with only `Receive(context.Context)` and
  `Perform(context.Context, Intent)`;
- deterministic fixed-size light/dark rendering and accessible line-oriented
  rendering;
- grapheme-safe editing, injected ordered key bindings, bounded history and
  activity, monotonic revision handling, and stale-update rejection;
- cancellation-aware Bubble Tea lifecycle and panic-contained one-shot session
  effects;
- a public-facade interaction proof that drives real Bubble Tea input through
  prompt submission, concurrent run cancellation, and clean Ctrl-Q shutdown;
- public `terminal.NewFixedRenderer` and `terminal.NewShell` factories whose
  signatures expose no Bubble Tea or internal types;
- canonical `@UIShell` and `@UIRenderer` provider annotations;
- explicit `/autoconfigure` fallback beans proven by committed generated Go; and
- an agent-friendly `tuittest` harness for canonical strict-JSON interaction
  traces, deterministic double replay, committed cell-accurate goldens,
  accessibility/contrast audits, keyboard-only complex-Unicode lifecycle
  coverage, non-authoritative embedded-font PNG review artifacts, bounded VT
  cell/cursor/alternate-screen conformance, and
  repository-owned real Unix PTY/Windows ConPTY acceptance (see
  [`docs/tuittest.md`](docs/tuittest.md)); and
- a removable, nested
  [`experiments/semantic-shell`](experiments/semantic-shell) Phase 7 proof that
  consumes the released public Session API using only the standard library and
  emits deterministic portable JSONL without Bubble Tea.

## Session boundary

An application injects an implementation of:

```go
type Session interface {
    Receive(context.Context) (SessionUpdate, error)
    Perform(context.Context, Intent) (CommandResult, error)
}
```

`Receive` owns one strictly increasing positive revision sequence. Updates are a
closed tagged union of complete snapshots, activity items, and prompt-history
replacements. Constructors validate all text and aggregate bounds and clone
caller-owned slices. A non-monotonic value stops the receive loop with a safe
visible error rather than spinning on a broken stream. Presentation invokes
each session operation exactly once: it does not reconnect, replay, or retry. A
transport client owns those policies. Implementations must be concurrency-safe
for one blocking receive, one ordinary operation, and one cancel-run operation;
the shell serializes calls within each lane.
Panics become the fixed error `session operation panicked`; panic values cannot
reach the terminal. Cancellation causes remain observable with `errors.Is`, but
a valid result or explicit error returned by the Session wins a concurrent late
cancellation so committed work is not misreported.
A `CommandResult` returned from `Perform` may not contain another intent.

## Direct composition

```go
renderer := terminal.NewFixedRenderer()
bindings, err := agenttui.StandardKeyBindings()
shell, err := terminal.NewShell(
    session,
    renderer,
    agenttui.DarkTheme(),
    bindings,
    initialView,
    streams,
    agenttui.NewTerminalConfig(accessible),
)
```

`TerminalConfig` is presentation-only and currently selects accessible mode.
Definition selection, revision selection, reconnect, and graceful daemon
shutdown belong to the future distribution/client runner. The constructor
snapshots the injected `Theme` SPI and key bindings, so later mutable provider
state cannot alter the running shell.

## Spice auto-configuration

Applications opt in explicitly:

```go
import _ "github.com/spice-framework/spice-agent-tui/autoconfigure"
```

The package contributes fallback beans for the fixed renderer, dark `Theme`,
eleven standard ordered `KeyBinding` values, connecting initial `ViewData`, OS
terminal streams, normal `TerminalConfig`, and the terminal `Shell`. It never
creates a fake session or a client configuration. Without an application-owned
exact `agenttui.Session` bean, the shell fallback remains inactive.

Key bindings are individual named collection elements because Spice collection
injection is `[]KeyBinding`, not an opaque slice provider. This preserves exact
generated order and gives embedding/tests typed per-bean overrides. Current
source-level collection selection does not back off fallback elements by bean
name; an application that needs a different binding set supplies its own Shell.
Duplicate actions and keystrokes fail during shell construction.

The committed `internal/spicegen/compositionproof` target is generated from an
external-package acceptance fixture. It proves direct construction, collection
order, exact Session injection, fallback activation, source mapping, generated
freshness, compilation, explicit `NewApplication` → `Start` → `Shell.Run` →
`Stop` normal exit, and shutdown without reflection or a runtime registry.

## Code-style boundary

The application-owned fixture at `internal/acceptance/composition` follows the
canonical Spice `java-structured` profile through the exact schema-2 policy in
`.spice/style.json`. The independent tools module pins the public Toolchain
preview that supplies `spicestyle`; `make check` and `make verify` run it
offline. The policy also names the real generated composition target so the
gate can distinguish generated ownership from handwritten input and reject a
generated-marker bypass. Runtime, presentation, `tuittest`, auto-configuration,
vendor, and the historical semantic-shell module retain their own contracts.
Because Toolchain preview4 configured roots cannot include the module-root
package `.`, `moduleOwnership` is the one documented inapplicable style rule;
the existing composition gate loads both root and fixture and independently
enforces their Modulith relationship.
See [`CODE_STYLE.md`](CODE_STYLE.md) and
[`docs/code-style.md`](docs/code-style.md).

## Annotations

Applications may define explicit providers with the TUI annotation tool:

```go
// @import { UIShell, UIRenderer } from "github.com/spice-framework/spice-agent-tui/annotation/ui"

// @UIShell(name="terminal", primary=true)
func NewApplicationShell(...) agenttui.Shell

// @UIRenderer(name="fixed", fallback=true)
func NewApplicationRenderer(...) agenttui.Renderer
```

The handlers consume shared compiler result facts and require the exact public
interface identity while preserving Go aliases. They never execute providers,
parse type-name strings, or add TUI behavior to the compiler. See
[`docs/annotations.md`](docs/annotations.md).

This repository still does not own a daemon, gRPC, operating-system IPC,
managed-daemon discovery, or a terminal executable. Those arrive through an
adopted high-level client and the distribution repository.

Go 1.26.5 is exact. Run `make tools-bootstrap` once on a fresh clone, `make
fast` for affected feedback, `make check` for the broad edit loop, and `make
verify` before a commit. Ordinary verification is offline.

## Release contract

`spice-release.json` is inert, canonical metadata for the centrally authorized
`go-module-v1` release profile. `make verify-release` runs the repository's
complete local gate. Tag pushes call the organization-owned keyless Go-module
release workflow at audited commit
`a56c451168aae0f2b3075782156d204d75fb7f69`. The
caller denies permissions by default, grants only contents, OIDC, attestations,
and artifact-metadata writes to that reusable job, and passes no secrets. The
repository quality gate rejects pin drift, excess permissions, local release
steps, additional jobs, or secret-based signing. The organization release
authority independently binds the repository name, module path, exact preview
version, required module graph, commit, and tag before it creates any artifact
or release.
