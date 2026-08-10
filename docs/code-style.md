# Code style

Application-owned Go follows the Spice `java-structured` profile. The
normative policy is the reviewed `CODE_STYLE.md` whose SHA-256 is
`8a58866fe06ff4e6bfc6d5d7cc9b01ef053d8d8dcbee5e4e7bef7f6cdcec15dd`.
That reviewed document codifies and reconciles the original supplied policy
document (`0947169de8263c2d3d8971d18a7f8bff4837b62eb3f4aec39de920fdabba0182`)
with the delivered Spice annotations and executable Toolchain verifier. The
reviewed policy is the authority; repositories must not carry divergent local
copies.

The independent tools module pins the exact `spicestyle` verifier at
`v0.1.0-preview.2.0.20260810184201-7e71c68fa312`. The repository-owned
`.spice/style.json` is strict schema-one configuration: unknown fields,
unsupported rules, broad variable exemptions, and unclassified package
functions fail closed. `make check` and `make verify` execute the analyzer
offline through that pinned tools graph.

The active migration boundary covers both process entrypoints and the
application-owned command, daemon lifecycle, daemon-process containment,
identity, terminal, connector, and TUI-session packages listed in
`.spice/style.json`. Within that boundary:

- each source file has one primary type and the filename follows that type;
- behavior belongs to the owning type instead of loose package functions;
- constructors are explicit and return errors last;
- mutable package state and `init` registration are forbidden;
- each exceptional Spice provider occupies one dedicated `*_bean.go` file;
- generated sources remain manifest-owned and are never edited by hand; and
- tests use only the exact Go testing entrypoint exception.

The source-root list is deliberately explicit while older packages are
migrated. Expanding it is a monotonic quality change: first refactor and test a
package, then add that package to the enforced roots in the same green commit.
Removing a governed root, weakening a rule, or adding a broad suppression is a
policy regression and must fail review.

The architecture proof is a conformance fixture rather than production
application behavior, but its handwritten behavior is governed by the same
profile. Its one exact `@Application` function is a named generated-package
fixture exception because it is not a command `main`; every provider and helper
still uses an ordinary dedicated bean or typed owner. That exception is not
precedent for production application code.
