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
