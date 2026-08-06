# Roadmap

Program phase status, ordering, and exit evidence live only in the canonical
[Spice Agent implementation ledger](https://github.com/spice-framework/spice-agent/blob/main/docs/implementation/README.md).
This repository does not maintain a second phase numbering scheme.

## Repository foundation

- [x] Establish the repository, ownership boundaries, Go 1.26.5 quality gate,
      and explicitly unselected compatibility metadata.
- [x] Pin exact compatible Spice core, toolchain, Agent, provider, and coding
      tool versions behind an executable generated architecture proof.
- [ ] Publish the distribution starter manifest and register release-target
      compatibility gates in the development catalog with the daemon/TUI slice.

The architecture-proof generated application backs the selected versions with
executable product code. The distribution manifest and release-target catalog
registration remain phase-specific daemon/TUI work in the canonical ledger.

## Distribution product

Its bounded distribution deliverables are two independently generated Spice
`@Application` targets: `spice-agentd` for explicit daemon service and
`spice-agent` for the TUI, explicit attach, and managed attach-or-start.
Version selection, generation, protocol behavior, cancellation, rollback,
cleanup, offline builds, debugging, packaging, and Windows/Linux evidence
follow the canonical ledger and are recorded there only after their gates pass.
