# Dependency and security review

## Product graph

The generated architecture proof selects exact immutable versions of Spice,
the Spice toolchain, Spice Agent, the OpenAI provider, and the coding-tool
starter. The provider pins `openai-go/v3 v3.50.0`; the coding tools pin
`x/sys v0.47.0`. Transitive versions and checksums are committed in `go.sum`
and `vendor/modules.txt`.

The Spice projects are Apache-2.0. Their repository-local reviews cover their
transitive Apache-2.0, BSD, and MIT dependencies. The distribution adds no
independent third-party runtime dependency. `make verify` runs gosec and
govulncheck over the selected product graph and reproduces vendor contents.

Native installed-byte verification directly imports
`github.com/Kodecable/crosspty v1.1.0` only from tests. The maintained public
TUI preview already selects that exact module. It is BSD-3-Clause, uses
`github.com/creack/pty v1.1.24` (MIT) on Unix, and uses Go `x/sys` on Windows;
all are checksum-pinned and covered by govulncheck. CrossPTY supports Linux PTY
and Windows 10 1809/Server 2019 or later ConPTY, exposes bounded graceful/forced
process-group cleanup, and receives only a closed secret-minimal environment.
The harness caps retained terminal output at 256 KiB and gives every wait,
close, and process lifecycle a context or explicit timeout. CrossPTY's nested
ActiveState, photostorm, and Go derivation licenses are deterministic source
inputs under `internal/releaseassets/attributions`; release-asset generation
copies them into `THIRD_PARTY_NOTICES.md` because ordinary `go mod vendor`
retains only the module-root license. No PTY dependency is linked into either
released executable.

Daemon event logging uses Spice Agent's standard-library-only production
logging package and the Spice core `slog.Handler` boundary. It owns one bounded
mailbox and lifecycle consumer, performs no network or file I/O, filters model
deltas before enqueue, and projects only typed metadata with process-local HMAC
correlation. The terminal does not subscribe to daemon events.

The proof exercises cancellation-aware kernel/provider/tool APIs, bounded
provider and filesystem payloads, reverse cleanup, secret-redacted provider
configuration, Windows-safe paths, and a local TLS transport. Its fixed test
credential is not a real secret and is never retained in events or generated
metadata. Coding tools retain bare process privileges; the product must display
their exported capability warning before the release application enables them.

Future product dependencies must document maintenance, Apache-2.0 license
compatibility, checksum and vulnerability status, cancellation and bounded
resource behavior, observability and secret handling, Windows/Linux support,
and executable compatibility evidence.

## Verification tools

The isolated tools module pins golangci-lint 2.12.2, gofumpt 0.10.0,
goimports/x-tools 0.48.0, gosec 2.28.0, govulncheck 1.1.4, and NilAway at
`f4f8ac24c032`. They are build-time-only and run without inherited credential
variables. The gate permits module network access only during explicit
preparation and runs later checks with `GOPROXY=off`, `GOWORK=off`, and the
local Go 1.26.5 toolchain.

The optional `make eval-opencode` developer harness is separately networked and
non-gating. It pins `opencode-ai@1.18.16` and every supported native package by
SHA-512, downloads registry archives without running npm lifecycle scripts, and
extracts only the expected executable. Before authenticated requests it also
requires the three exact OpenRouter `:free` routes to advertise zero prompt and
completion prices and tool support. No evaluator package enters the product Go
module or release graph; see [Advisory OpenCode evaluation](opencode-evaluation.md).
