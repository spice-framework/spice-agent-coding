# Dependency and security review

## Product graph

Phase 0 uses only the Go standard library and exports no runtime behavior. It
does not depend on Spice, the toolchain, an agent protocol, a daemon, or the
TUI because none of those compatibility contracts has been selected.

Every future product dependency must document maintenance, Apache-2.0 license
compatibility, checksum and vulnerability status, cancellation and bounded
resource behavior, observability and secret handling, Windows/Linux support,
and the executable compatibility evidence that justifies the selected version.

## Verification tools

The isolated tools module pins golangci-lint 2.12.2, gofumpt 0.10.0,
goimports/x-tools 0.48.0, gosec 2.28.0, govulncheck 1.1.4, and NilAway at
`f4f8ac24c032`. They are build-time-only and run without inherited credential
variables. The gate permits module network access only during explicit
preparation and runs later checks with `GOPROXY=off`, `GOWORK=off`, and the
local Go 1.26.5 toolchain.
