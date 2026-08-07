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

The distribution will also contain two inspectable, independently generated Spice
applications: `spice-agentd`, which supports explicit daemon serve, and
`spice-agent`, which supports explicit attach and managed attach-or-start. The
managed launcher cleans up only a daemon it starts. Coding tools still run with
the selected user process's privileges; that security property is not a ban on
the supported daemon process.

The transport-neutral argument contract is implemented and tested in
`internal/daemoncommand` and `internal/terminalcommand`. It provides strict
serve, attach, managed, help, and check modes through injected runners without
inventing the not-yet-adopted daemon/client transport APIs. It also establishes
fixed secret-safe diagnostics and the mandatory bare-privilege warning. See
[the command seam](docs/command-seam.md).

Go 1.26.5 is exact. On a fresh clone, run `make tools-bootstrap` once to
populate the exact product and tools module graphs without changing tracked
module files. All ordinary quality targets remain offline. Use `make fast`,
`make check`, and `make verify`.

The generated proof is committed for inspection. Do not edit it directly;
change `internal/architectureproof`, then run:

```text
go tool github.com/spice-framework/toolchain/cmd/spice generate --target ArchitectureProof . ./internal/architectureproof
```
