# Spice Agent Coding

This repository provides the installable Spice Agent coding distribution. Its
first executable slice is the generated
`internal/spicegen/architectureproof` application: the real OpenAI Responses
adapter, explicitly activated coding-tool starter, and deterministic Agent
kernel are assembled through ordinary Spice beans. The offline acceptance test
executes a local TLS Responses endpoint, a compiled `read` call, provider
continuation, and final event delivery without credentials or external network
access. See [the architecture-proof evidence](docs/architecture-proof.md).

The distribution will also contain two inspectable, independently generated Spice
applications: `spice-agentd`, which supports explicit daemon serve, and
`spice-agent`, which supports explicit attach and managed attach-or-start. The
managed launcher cleans up only a daemon it starts. Coding tools still run with
the selected user process's privileges; that security property is not a ban on
the supported daemon process.

Go 1.26.5 is exact. On a fresh clone, run `make tools-bootstrap` once to
populate the exact product and tools module graphs without changing tracked
module files. All ordinary quality targets remain offline. Use `make fast`,
`make check`, and `make verify`.

The generated proof is committed for inspection. Do not edit it directly;
change `internal/architectureproof`, then run:

```text
go tool github.com/spice-framework/toolchain/cmd/spice generate --target ArchitectureProof . ./internal/architectureproof
```
