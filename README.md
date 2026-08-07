# Spice Agent Coding

Unified documentation: [spiceframework.dev/agent/distributions/coding](https://spiceframework.dev/agent/distributions/coding/).

This repository provides the installable Spice Agent coding distribution. Its
first executable slice is the generated
`internal/spicegen/architectureproof` application: the real OpenAI Responses
adapter, explicitly activated coding-tool starter, and deterministic Agent
kernel are assembled through ordinary Spice beans. The generated graph owns an
explicit static tool-plan source and portable snapshot compatibility identity;
every run reports the exact compiled identities, tool generation, and combined
fingerprint selected by the graph. The offline acceptance test
executes a local TLS Responses endpoint, a compiled `read` call, provider
continuation, final event delivery, and provider cancellation without
credentials or external network access. See
[the architecture-proof evidence](docs/architecture-proof.md).

The distribution now also contains the independently generated `spice-agentd`
application. Its inspectable graph constructs typed configuration, the OpenAI
provider, compiled coding tools, the deterministic engine, the run host, the
authenticated gRPC server, and current-user local IPC. `serve` publishes only
after the accept loop is live; `--check` constructs and cleans the same graph
without publishing. See [the generated daemon target](docs/daemon-target.md).

The second product target, `spice-agent`, is also generated and inspectable. It
assembles the Bubble Tea shell, rendering defaults, protocol session adapter,
explicit local attach, and managed attach-or-start through ordinary Spice
beans. Construction is I/O-lazy; `--check` builds and reverses the exact graph
without connecting or starting a daemon. Managed mode cleans up only a daemon
candidate it owns. See [the generated terminal target](docs/terminal-target.md).

Coding tools run with the selected user process's privileges and are not
sandboxed. Their generated beans receive an explicit executable resolver and
native process launcher. Windows attaches each child to a kill-on-close Job
before it runs. Unix registers each direct child with the adopted daemon root
and combines a process group with PID-plus-birth-identity tracking; an
uncooperative fork that detaches between process-table samples remains outside
this unprivileged guarantee.

The transport-neutral argument contract is implemented and tested in
`internal/daemoncommand` and `internal/terminalcommand`. It provides strict
serve, attach, managed, help, and check modes through injected runners without
reflecting arguments or implementation failures into diagnostics. Both
commands now drive independently generated applications. See
[the command seam](docs/command-seam.md).

Go 1.26.5 is exact. On a fresh clone, run `make tools-bootstrap` once to
populate the exact product and tools module graphs without changing tracked
module files. All ordinary quality targets remain offline. Use `make fast`,
`make check`, and `make verify`.

Generated applications are committed for inspection. Do not edit them
directly. Change the corresponding handwritten composition and run:

```text
go tool github.com/spice-framework/toolchain/cmd/spice generate --target ArchitectureProof . ./internal/architectureproof
go tool github.com/spice-framework/toolchain/cmd/spice generate --target spice-agentd ./cmd/spice-agentd
go tool github.com/spice-framework/toolchain/cmd/spice generate --target spice-agent ./cmd/spice-agent
```

The daemon and terminal development supervisors are intentionally separate:

```text
make dev-daemon
make dev-terminal
```

Run them in separate terminals. Each command uses `spice dev`, keeps its own
last-known-good process alive when analysis, generation, or build fails, and
gracefully replaces only that process after a valid change. Target-local
source paths are excluded from the other watcher; shared module or composition
changes correctly invalidate both applications.
See [the development-loop contract](docs/development-loop.md).
