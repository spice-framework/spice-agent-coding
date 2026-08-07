# Architecture

## Distribution ownership

The coding distribution contains two independently generated Spice application
targets:

```text
spice-agentd @Application             spice-agent @Application
        |                                      |
        v                                      v
inspectable generated daemon          inspectable generated TUI/launcher
        |                                      |
        |-- authenticated local IPC            |-- explicit attach
        |-- engine and run lifecycle            |-- managed attach-or-start
        |-- configuration and policy            |-- terminal lifecycle
        `-- graceful shutdown                   `-- owned-child cleanup
```

`spice-agentd` supports explicit serve. `spice-agent` supports explicit attach;
its default managed mode attaches to a compatible daemon or starts one. An
attached daemon remains externally owned. A daemon started by managed mode is a
supported process whose lease and cleanup belong to that launcher instance.
Each target independently owns dependency construction, configuration,
cancellation, observability, rollback, and cleanup for its boundary.

`internal/distribution` owns packaging. `internal/daemon` owns the handwritten
daemon composition source, while `internal/spicegen/daemon` and
`.spice/daemon.manifest.json` are generator-owned. `internal/terminal` will
compose the separately versioned TUI client.

Explicit serve and attach are supported lifecycle modes. Internal packages are
not alternative entrypoints, and neither generated target may bypass the other
target's protocol boundary. The security phrase "bare user-process privileges"
applies to coding tools: they execute with the selected user process's authority
and are not sandboxed. It does not describe or prohibit the daemon process
topology.

`internal/daemoncommand` and `internal/terminalcommand` define the
transport-neutral CLI boundaries. They parse
only the documented grammar into immutable values and invoke injected runners
with caller-owned contexts. They do not import daemon, client, gRPC, IPC, or
generated packages. Detailed runner errors, raw arguments, and endpoint values
are never reflected through the public command diagnostics. See
[the command seam](docs/command-seam.md).

`cmd/spice-agentd` adapts `internal/daemoncommand` to the generated daemon
application. Construction opens protected state without binding or publishing.
Start binds current-user local IPC, enters the gRPC accept loop, and only then
publishes authenticated endpoint metadata. Shutdown withdraws that exact
publication before stopping admission, closing the listener, draining the host,
and reversing generated ownership. A generated root-registry bean is adopted
before the daemon root and coding-tool beans. This proves lifecycle order but
does not yet intercept tool process starts; the public injected launcher SPI and
coding-tool integration remain required before Unix descendant containment is
accepted. See [the daemon target](docs/daemon-target.md).

`internal/architectureproof` is a real generated application used to freeze the
SDK composition boundary before daemon and terminal commands are added. It
proves a normal provider replacing a fallback provider, three explicit
auto-configured coding-tool beans, a canonical `map[string]tool.Tool`, reverse
cleanup, and the provider → tool → provider continuation loop. The graph also
constructs the dispatcher, source-guaranteed static `stage.ToolPlanSource`,
explicit unavailable interaction broker, execution-plan metadata, and engine as
ordinary inspectable beans. The metadata records every executable provider,
kernel/dispatch stage, broker, and static tool with its selected module version;
there are no observer or dispatcher-decorator beans in this proof. A semantic
snapshot compatibility identity is explicit and stable across machines, while
the kernel combines it with the exact leased tool generation and definition
fingerprints for each run. Generated code
lives only under `internal/spicegen/architectureproof`; the ownership manifest
and source mappings live in `.spice/architectureproof.manifest.json`.

## Compatibility

`compatibility.json` records Go 1.26.5 plus the exact immutable Spice,
toolchain, Agent, OpenAI provider, and coding-tools selections exercised by the
architecture proof and generated daemon. The TUI remains explicitly null until
the terminal target adopts it. Replacing any selection requires executable
compatibility tests.
