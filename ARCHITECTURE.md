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

The daemon explicitly blank-imports the Agent runtime-plugin host
auto-configuration. Its generated graph constructs the compiled read,
replace, and shell beans first, snapshots them into the fallback compiled
dispatcher, and then constructs one exact `pluginhost.Host`. That same pointer
is adapted to `stage.ToolPlanSource` for the engine; there is no parallel
registry or compiled `RuntimeGraph`. Host construction performs no plugin
discovery or launch. Future runtime-plugin activation can only publish a new
immutable tool generation through this generated host, while generated reverse
cleanup drains its leases before releasing the shared contained process
launcher and root registry.

`internal/distribution` owns packaging. `internal/daemon` and
`internal/terminal` own the handwritten compositions. Their generated packages
live under `internal/spicegen/spice_agentd` and
`internal/spicegen/spice_agent`; exact ownership and source mappings live in
`.spice/spice_agentd.manifest.json` and `.spice/spice_agent.manifest.json`.

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

`cmd/spice-agentd` is the package-main generation bridge and adapts
`internal/daemoncommand` to the generated daemon
application. Construction opens protected state without binding or publishing.
Start binds current-user local IPC, enters the gRPC accept loop, and only then
publishes authenticated endpoint metadata. Shutdown withdraws that exact
publication before stopping admission, closing the listener, draining the host,
and reversing generated ownership. A generated root-registry bean is adopted
before the daemon root and coding-tool beans. The graph injects a
distribution-owned executable resolver and process launcher into the shell
tool and the runtime-plugin host. Windows creates a suspended child, assigns it
to a kill-on-close Job, then resumes it. Unix establishes a new process group,
registers the direct child with the adopted root, and tracks observed
descendants by PID and immutable birth identity. This is lifecycle containment,
not a sandbox; Unix cannot universally observe an adversarial fork-and-detach
between snapshots. See
[the daemon target](docs/daemon-target.md).

`cmd/spice-agent` is the package-main generation bridge and adapts
`internal/terminalcommand` to the separately generated
terminal application. The graph explicitly selects the local endpoint store,
managed discovery and startup lock, daemon starter, one exact client connector,
the protocol-1.3 initialization request, UI-neutral session adapter, and the
TUI repository's fallback renderer/theme/key-binding/shell beans. Managed mode
attaches to a compatible daemon or serializes one owned sibling-daemon start;
explicit mode never starts a daemon. Construction performs no discovery,
connection, or process start, so `--check` can build and reverse the complete
graph safely. The session owns event/interaction streams, publishes monotonic
bounded updates, never retries mutations, and resumes event streams only from
the last published sequence. See [the terminal target](docs/terminal-target.md).

Both command packages use the Spice application-package layout required by
`spice dev`. Repository-owned `dev-daemon` and `dev-terminal` targets start
independent last-known-good supervisors and exclude only source owned solely by
the other product target. Shared-source changes remain visible to both. See
[the development-loop contract](docs/development-loop.md).

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
toolchain, Agent, OpenAI provider, coding-tools, and TUI selections exercised by
the architecture proof and both generated product targets. Replacing any
selection requires executable compatibility tests.
