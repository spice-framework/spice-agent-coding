# Code style

Application-owned Go follows the Spice `java-structured` profile. The
normative policy is the reviewed `CODE_STYLE.md` owned by Spice commit
`0e79bc4f3b294cd0a429598c4921391f2e4d10e2`; its canonical SHA-256 is
`09c014e2d7eb93bf2b395e24e4e6ff2466c05d164d4778a11cf7433164bffb76`.
Repositories do not carry divergent local policy copies.

The product graph selects public Spice `v0.1.0-preview.4` at commit
`9970279b7a2f029abd140ba6ee45df554cb159e2`. The independent tools module pins
the exact `spicestyle` verifier from Toolchain `v0.1.0-preview.7` at commit
`e83e4ff8639ed6e3aa49c6dd8b2e3ba0d5174e08`. Its module and `go.mod` sums are
`h1:XgNwiSCrnwh+iDxi3RJX8pbRTTpdL7NDiMedE861U6g=` and
`h1:nezzFkAq9TDdavVL5sYJm2nOKNWAu1p9VTz3XFihgUg=`. The repository-owned
quality gate checks the product and tools selections independently.

`.spice/style.json` is strict schema-two configuration. Its two handwritten
roots are exactly `cmd` and `internal`; `internal/spicegen` is the generated
root. Unknown fields, unsupported rules, selection mutations, broad variable
exemptions, and unclassified package functions fail closed. Every supported
rule is an error and the type-file limit is 500 lines.

Six sorted build selections cover `darwin`, `linux`, and `windows` on `amd64`
with CGO disabled. Each platform has a default selection and an exact
`spice_acceptance` selection. Toolchain derives the two generated command
entrypoints and the ArchitectureProof application independently, producing 18
selection-and-application scopes without merging provider, configuration, or
environment-key universes. The compiler-owned `spice_generate` overlay is not
a seventh public selection.

The complete handwritten application boundary follows these invariants:

- each source file has one primary type and the filename follows that type;
- behavior belongs to the owning type instead of loose package functions;
- constructors are explicit and return errors last;
- mutable package state and `init` registration are forbidden;
- each exceptional Spice provider occupies one dedicated `*_bean.go` file;
- every managed provider declares its lifetime explicitly;
- all 21 internal package roots and both command roots declare exact `@Module`
  ownership, with cross-module dependencies allowlisted by full Go identity;
- generated sources remain manifest-owned and are never edited by hand; and
- tests use only the exact Go testing entrypoint exception.

The exact `distribution.Commit` and `distribution.Version` string variables
are the only package-variable exceptions. Go release builds require those
stable linker `-X` symbols; application behavior consumes an immutable
constructed identity snapshot.

The package-function exceptions are limited to Go entrypoints, dedicated Spice
provider and topic contributions, test functions, the two compiler-validated
generated application bridges, and the exact ArchitectureProof application
fixture. The latter is a conformance target rather than production application
behavior and is not precedent for loose application functions.

The `spice_acceptance && !spice_generate` command companions use the canonical
`*_spice_acceptance.go` suffix. Each companion has one filename-matched primary
type, constructors use the exact `New<Type>` form and return that owned type,
and all other fixture behavior is receiver-owned. Default-build structural
tests enforce the constrained inventory while tagged tests preserve the real
acceptance environment and provider-validation protocol.

`make check` and `make verify` invoke `spicestyle` through the independent tools
module with `--config=.spice/style.json`. The gate deliberately contaminates
ambient Go target, tag, toolchain, proxy, experiment, authentication, tuning,
and FIPS variables. Toolchain must produce the same clean configured analysis,
must start annotation tools on the host toolchain, and must not download hidden
tools or modules.
