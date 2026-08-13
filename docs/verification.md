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
  The repository-owned `verify` mode has one fail-closed 30-minute outer
  deadline, and the hosted Quality job has a 40-minute boundary that includes
  dependency bootstrap. Other ordinary quality modes retain their 15-minute
  outer deadline; the separate OpenCode evaluator retains its own bounded
  evaluation deadline.
- `make verify-release` is the unconditional alias consumed by the central
  distribution workflow. The repository identity gate requires this exact
  alias before any release tag is considered valid.
- `make verify-release-artifacts` is a separate, tag-independent deterministic
  installed-byte gate. Set `SPICE_DISTRIBUTION_VERIFIED_ARTIFACT_DIR` to a canonical absolute
  directory containing exactly the nine subjects already authenticated by the
  independent Toolchain verifier: six archives, release metadata, SPDX SBOM,
  and `checksums.txt`. The Sigstore bundle is verification evidence rather than
  an attested subject and must not be placed in that directory. This gate never
  downloads or authenticates artifacts itself.

The release-artifact gate rejects missing, extra, non-regular, or symlinked
subjects; noncanonical checksums; metadata, target, payload, or SBOM drift; and
archive traversal, duplicate entries, undeclared paths, invalid modes, sizes,
or payload hashes. It extracts all six archives twice beneath distinct clean
paths containing spaces and Unicode and requires exact installed membership,
bytes, and modes. Cross-architecture binaries are never executed. It extracts
the native archive beneath a path containing
spaces and Unicode, runs both exact binaries with `--version` and `--check`,
then proves explicit `serve`/`attach` and zero-argument managed sibling startup
and cleanup. It also drives the exact terminal bytes through Linux PTY or
Windows ConPTY at 80x24, interprets a bounded transcript with the public TUI
virtual terminal, resizes both layers to 100x30, types Unicode input, replaces
an explicit daemon while preserving terminal history, and proves ANSI,
alternate-screen, cursor, clean quit, and managed-sibling cleanup. There is no
pipe fallback or accessible-mode runtime flag in this native boundary. Linux
uses private `XDG_RUNTIME_DIR` and `XDG_CONFIG_HOME` roots.
The managed case deliberately gives the outer PTY owner direct-process-only
termination, so a Windows Job or Unix process-group teardown cannot satisfy
the sibling assertion; a separately retained process witness owns only
failure cleanup. Provider request and response counts remain exactly one
before and after daemon replacement and after terminal quit.
The raw transcript remains exact and bounded; only Bubble Tea's two terminal
capability queries are withheld from the output-only emulator, which cannot
own or answer an input-device negotiation.
Windows deliberately exercises the default current-user endpoint and authority
and therefore fails unless `SPICE_DISTRIBUTION_EPHEMERAL_RUNNER=1` is supplied
by a disposable runner. Linux and macOS require that acknowledgement to be
empty. The retired `SPICE_AGENT_VERIFIED_ARTIFACT_DIR` and
`SPICE_AGENT_EPHEMERAL_RUNNER` aliases are rejected. The gate is an installed
execution check, not a replacement for provenance or checksum verification.

One unconditional Phase 6 suite entrypoint runs the all-six installation,
native PTY/ConPTY replacement-and-history proof, five-sample installed
performance budget, and deterministic decisive workflow through compiled read, replace, and shell
tools, the required runtime plugin, provider/compiled-process/plugin
cancellation, reconnect fencing, exactly-once terminals, privilege warnings,
and secret scans. See [Phase 6 installed release evidence](phase6-release-evidence.md).

`make verify-release-live` is the separate, credential-gated external provider
proof. It uses the same independently verified artifact directory but is never
called by `make fast`, `make check`, `make verify`, `make verify-release`,
or `make verify-release-artifacts`. Set
`SPICE_DISTRIBUTION_LIVE_PROVIDER=1`, `OPENAI_API_KEY`, and `OPENAI_MODEL`
only for an explicitly authorized invocation. Windows additionally requires
the existing ephemeral-runner acknowledgement. The quality gate passes only
those values plus optional OpenAI base URL, organization, and project settings
to the exact release process.
After shutdown, the live proof scans bounded terminal/daemon output, endpoint
metadata, and all test-owned persisted roots for the credential canary.

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

`make eval-opencode` is a separate, networked, advisory developer harness. It
downloads and integrity-checks the exact OpenCode evaluator, runs only exact
zero-cost OpenRouter `:free` routes inside disposable repository and home/config
roots, and reports a deterministic local rubric. It is intentionally excluded
from every normal and release verification target. See
[Advisory OpenCode evaluation](opencode-evaluation.md).
