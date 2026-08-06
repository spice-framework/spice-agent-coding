# Generated architecture proof

## Contract

`internal/architectureproof` is an application-owned Spice graph. It blank
imports the OpenAI and coding-tool auto-configuration packages, contributes a
normal `model.Provider`, and consumes the generated canonical
`map[string]tool.Tool`. It contains no runtime registry, reflection-based
lookup, package scan, or hidden dependency construction.

The dispatcher, `stage.StaticToolPlanSource`, unavailable interaction broker,
execution-plan metadata, and kernel engine are separate generated beans. The
tool source is the trusted owner of a source-guaranteed immutable generation:
new runs lease its current generation, snapshot recovery must request that exact
generation, and every terminal or rollback path releases its lease. The plan
metadata lists every executable provider, kernel/dispatch stage, broker, and
static tool with the selected module version. This proof intentionally has no
observer or dispatcher-decorator bean, so it does not invent placeholder
identities for absent behavior.

The generated graph supplies a machine-independent semantic
`SnapshotCompatibilityIdentity`. At run start the kernel combines it with the
sorted compiled identities, exact static tool-plan ID, and immutable tool
definition fingerprints. Acceptance asserts all four values rather than
trusting configuration text alone.

The provider uses `openaiprovider.New` with an instance-owned HTTP client. Its
local TLS fixture emits a Responses function call for `read`; the generated
graph dispatches the real bounded coding tool; the kernel appends the result;
and a second Responses request emits the final text. The fixture records only
safe Boolean/protocol facts. The test credential is a fixed non-secret value
and never enters an event, generated file, manifest, or retained request body.
An independent generated-application run holds the real provider request open,
cancels the caller-owned context, and requires exactly one model, turn, and run
terminal outcome while the server observes request cancellation.

## Generated evidence

- `.spice/architectureproof.manifest.json` owns every output and maps provider
  construction back to application or starter source.
- `internal/spicegen/architectureproof/spice_providers_gen.go` contains direct
  constructor calls, the literal named tool map, and direct plan-source,
  metadata, broker, and engine construction.
- `spice beans --explain` reports the OpenAI starter default as replaced and
  the read, replace, and shell defaults as selected.
- `spice generate --check` and `spice generate --diff` both require the
  committed output to be byte-current.
- Acceptance scans emitted event payloads, generated providers, and the
  ownership manifest for the fixture credential.

## Verification

`make fast` runs the generated workflow and remains targeted below 30 seconds
on a warm development machine. `make check` adds formatting, module/vendor,
and vet checks. `make verify` additionally runs lint, nil safety, security,
shuffled/race tests, handwritten coverage, and vendor-offline build/test. The
canonical generated package is compiled and executed but excluded from the
handwritten statement-coverage denominator.

This proof intentionally does not implement the daemon or TUI. It freezes the
cross-repository static-composition seam those applications will consume.
