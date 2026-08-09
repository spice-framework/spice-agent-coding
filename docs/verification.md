# Verification

On a fresh clone, explicitly populate the exact product and tools module graphs:

```text
make tools-bootstrap
```

This is the only network-enabled quality mode. It requires Go 1.26.5, validates
the repository identity and exact tool pins, downloads `all` from private
temporary copies of both `go.mod`/`go.sum` pairs, disables Go authentication,
and permits only the public checksum database and module proxy. It verifies that
the repository is byte-for-byte unchanged even when a download fails. A
repository without a tools module is valid. No API keys, tokens, passwords, or
secrets are passed to the Go subprocess.
Every child Go command uses the selected Go 1.26.5 binary from `runtime.GOROOT`,
not an older `go` that may appear first on `PATH`.

- `make fast` validates repository identity and runs shuffled product tests while
  excluding only the process-heavy real-CLI development acceptance.
- `make check` adds formatting, module/vendor consistency, vet, and the complete
  shuffled suite, including the real `spice dev` process workflow.
- `make verify` adds lint, NilAway, gosec, govulncheck, race tests, coverage, and
  vendor-offline tests/builds. It compiles and executes committed Spice output
  while excluding canonical generated statements from the handwritten 85%
  coverage denominator. The coverage invocation does not re-run the two
  zero-statement, process-heavy acceptance packages: `make check`, shuffled,
  race, and vendor-offline gates already execute them, and their child-process
  work cannot contribute in-process statement coverage.
- `make verify-release` is the unconditional alias consumed by the central
  distribution workflow. The repository identity gate requires this exact
  alias before any release tag is considered valid.

The architecture-proof acceptance additionally runs `spice generate --check`,
`spice generate --diff`, and `spice beans --explain` against the selected
vendored toolchain. It then executes the generated provider → read tool →
provider continuation locally without external network access.

`make check` and `make verify` also regenerate the third-party notices and
transitive Protobuf descriptor set in memory and require byte-identical
committed release assets. Refresh them intentionally with
`go run ./internal/releaseassets`; the generator is deterministic and operates
only on the committed vendor and compiled protocol graphs.

The repository-owned verifier is cross-platform. `make fast`, `make check`, and
`make verify` force `GOPROXY=off`; missing cache entries fail instead of causing
hidden downloads. `make fmt` is the only target that rewrites Go source.
