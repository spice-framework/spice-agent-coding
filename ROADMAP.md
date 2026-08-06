# Roadmap

Program phase status, ordering, and exit evidence live only in the canonical
[Spice Agent implementation ledger](https://github.com/spice-framework/spice-agent/blob/main/docs/implementation/README.md).
This repository does not maintain a second phase numbering scheme.

Its bounded distribution deliverables are two independently generated Spice
`@Application` targets: `spice-agentd` for explicit daemon service and
`spice-agent` for the TUI, explicit attach, and managed attach-or-start.
Version selection, generation, protocol behavior, cancellation, rollback,
cleanup, offline builds, debugging, packaging, and Windows/Linux evidence
follow the canonical ledger and are recorded there only after their gates pass.
