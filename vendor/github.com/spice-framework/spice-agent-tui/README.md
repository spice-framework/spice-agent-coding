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
- public `terminal.NewFixedRenderer` and `terminal.NewShell` factories whose
  signatures expose no Bubble Tea or internal types;
- canonical `@UIShell` and `@UIRenderer` provider annotations; and
- explicit `/autoconfigure` fallback beans proven by committed generated Go.

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
