# Generated architecture proof

## Contract

`internal/architectureproof` is an application-owned Spice graph. It blank
imports the OpenAI and coding-tool auto-configuration packages, contributes a
normal `model.Provider`, and consumes the generated canonical
`map[string]tool.Tool`. It contains no runtime registry, reflection-based
lookup, package scan, or hidden dependency construction.

The provider uses `openaiprovider.New` with an instance-owned HTTP client. Its
local TLS fixture emits a Responses function call for `read`; the generated
graph dispatches the real bounded coding tool; the kernel appends the result;
and a second Responses request emits the final text. The fixture records only
safe Boolean/protocol facts. The test credential is a fixed non-secret value
and never enters an event, generated file, manifest, or retained request body.

## Generated evidence

- `.spice/architectureproof.manifest.json` owns every output and maps provider
  construction back to application or starter source.
- `internal/spicegen/architectureproof/spice_providers_gen.go` contains direct
  constructor calls and the literal named tool map.
- `spice beans --explain` reports the OpenAI starter default as replaced and
  the read, replace, and shell defaults as selected.
- `spice generate --check` and `spice generate --diff` both require the
  committed output to be byte-current.

## Verification

`make fast` runs the generated workflow and remains targeted below 30 seconds
on a warm development machine. `make check` adds formatting, module/vendor,
and vet checks. `make verify` additionally runs lint, nil safety, security,
shuffled/race tests, handwritten coverage, and vendor-offline build/test. The
canonical generated package is compiled and executed but excluded from the
handwritten statement-coverage denominator.

This proof intentionally does not implement the daemon or TUI. It freezes the
cross-repository static-composition seam those applications will consume.
