# Architecture

## Ownership and dependency direction

This repository owns the terminal product, not the daemon, engine protocol,
transport, client discovery, or process supervisor.

```text
public immutable values and Session
                 |
                 v
public terminal facade
                 |
                 v
internal Bubble Tea presentation

explicit /autoconfigure --> ordinary generated Spice bean graph
public TUI annotations --> v1alpha2 tool --> generic Spice compiler
```

The root package owns bounded UI-neutral values and interfaces. The public
`terminal` package owns the only production renderer and shell constructors.
`internal/presentation` owns Bubble Tea models, messages, commands, rendering,
and program lifecycle. No exported function or interface contains a Bubble Tea
type.

## Session and effects

`Session` is the narrow adopted TUI-facing client boundary. It has only
`Receive(context.Context) (SessionUpdate, error)` and
`Perform(context.Context, Intent) (CommandResult, error)`. Implementations own
transport, reconnect, replay, endpoint discovery, and daemon lifecycle. They
must support one blocking Receive, one ordinary Perform, and one cancel-run
Perform concurrently; presentation serializes calls within each lane.

Every received update carries one positive global revision. A `SessionSnapshot`
contains workspace, status, bounded activity, and bounded prompt history.
`SessionUpdate` is a tagged union of snapshot, activity, and prompt-history
payloads. Constructors clone caller slices and validate UTF-8, terminal safety,
item limits, aggregate view size, and prompt limits. The Session contract
requires strictly increasing revisions. An invalid or non-monotonic received
value stops rearming and produces a fixed safe error status.

`sessionEffects` is the sole adapter to private Bubble Tea messages. Each
command invokes `Receive` or `Perform` once and never retries. It validates the
returned update or result, rejects nested result intents, preserves caller
cancellation causes before invocation and during a panic, and converts panics
to a fixed non-sensitive error. A valid result or explicit error returned by
the Session wins a concurrent late cancellation so a committed mutation is
never misreported as cancelled. The model performs no I/O in `Update`; one
receive is armed at a time, ordinary work and cancellation use independent
bounded control lanes, operation tokens reject stale completions, and successful
prompt submission commits local history exactly once.

## Presentation

The fixed renderer is pure: a semantic snapshot, bounded size, and immutable
theme snapshot produce the same fixed-size frame. Accessible mode emits stable
semantic lines without ANSI, alternate-screen presentation, cursor control, or
resize-only replay. Normal mode has exact display-cell sizing and light/dark
palettes.

Prompt editing moves on Unicode grapheme boundaries. Activity is an
oldest-first-evicted bounded window. Prompt history is capped at 64 entries.
Injected key bindings are copied, validated in deterministic order, and reject
duplicate actions and keystrokes. Ctrl-C/Ctrl-Q quit, Escape/Ctrl-X cancel the
active run, Enter submits, and Alt-Enter responds.

`terminal.NewShell` accepts only public interfaces and immutable values. It
validates the initial view, snapshots the `Theme` SPI through `NewTheme`, copies
bindings through model construction, adapts the injected Session, and delegates
to the private Bubble Tea shell. It never discovers a session. `TerminalConfig`
contains only accessibility presentation policy; definition selection and
shutdown bounds were removed because this layer did not consume them.

## Spice-native composition

Blank-importing `github.com/spice-framework/spice-agent-tui/autoconfigure`
contributes replaceable fallback beans for:

- exact `Renderer` and `Theme` interfaces;
- eleven named, ordered `KeyBinding` interface beans;
- connecting `ViewData`, OS `TerminalIO`, and normal `TerminalConfig`; and
- exact `Shell`, conditional on an application-owned exact `Session` bean.

There is no default Session and no client configuration. Dependency presence
alone activates nothing. The eleven binding beans intentionally use Spice's
native `[]KeyBinding` collection semantics; an opaque `[]KeyBinding` provider
would be a different bean type and would not populate that collection.

The committed `CompositionProof` generated target is the executable
architecture assertion. It proves blank-import discovery, fallback selection,
exact interface injection, collection order, direct factories, source mappings,
ownership manifests, byte-identical regeneration, external-package startup,
an actual terminal normal exit through the explicit
`NewApplication` → `Start` → `Shell.Run` → `Stop` workflow, and shutdown.
Generated Go is not hand-edited. The generic generated `Application.Run` is not
the terminal runner; the distribution owns that explicit orchestration.

## Annotation SDK

`annotation/ui` is the named `annotations` interface. Each annotation has one
canonical descriptor/handler file. The authorized annotation tool returns only
generic provider and bean metadata. Shared `go/types` result facts enforce exact
canonical `Shell` or `Renderer` identities, including aliases; wrappers,
anonymous interfaces, concrete outputs, malformed facts, and unsupported
cleanup/error layouts fail closed. The handlers never parse declaration type
strings or execute application code.

`terminal` and `autoconfigure` are explicit Modulith named interfaces. Other
descendant packages remain internal to the module.

## Compatibility boundary

`compatibility.json` records Go 1.26.5, local pre-release UI contracts, and the
exact Spice core/toolchain revisions. The high-level daemon client module is
still intentionally null: this repository now defines the TUI-facing Session
SPI but does not select a transport implementation. Replacing that null entry
requires an adopted version and end-to-end daemon compatibility proof.
