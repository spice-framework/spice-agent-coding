# Roadmap

Program phase status, ordering, and exit evidence live only in the canonical
[Spice Agent implementation ledger](https://github.com/spice-framework/spice-agent/blob/main/docs/implementation/README.md).
This repository does not maintain a second phase numbering scheme.

## Repository foundation

- [x] Establish the repository, ownership boundaries, Go 1.26.5 quality gate,
      and explicitly unselected compatibility metadata.
- [ ] When executable product slices exist, pin exact compatible Spice core and
      toolchain versions, publish the distribution starter manifest, and
      register compatibility and verification gates in the development catalog.

The checked item describes this repository's scaffold only. Its portion of the
canonical multi-repository foundation is not complete until the pending pins,
manifest, and catalog registration are backed by executable product code.

## Distribution product

Its bounded distribution deliverables are two independently generated Spice
`@Application` targets: `spice-agentd` for explicit daemon service and
`spice-agent` for the TUI, explicit attach, and managed attach-or-start.
Version selection, generation, protocol behavior, cancellation, rollback,
cleanup, offline builds, debugging, packaging, and Windows/Linux evidence
follow the canonical ledger and are recorded there only after their gates pass.
