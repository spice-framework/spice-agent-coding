# Spice Agent Coding

This repository will provide the installable Spice Agent coding distribution.
It is currently a repository foundation: governance, ownership, compatibility
metadata, and the Go quality contract are implemented; runtime APIs, commands,
and generated application files are intentionally absent. Program status lives
in the canonical [Spice Agent implementation ledger](https://github.com/spice-framework/spice-agent/blob/main/docs/implementation/README.md).

The distribution will contain two inspectable, independently generated Spice
applications: `spice-agentd`, which supports explicit daemon serve, and
`spice-agent`, which supports explicit attach and managed attach-or-start. The
managed launcher cleans up only a daemon it starts. Coding tools still run with
the selected user process's privileges; that security property is not a ban on
the supported daemon process.

Go 1.26.5 is exact. Use `make fast`, `make check`, and `make verify`.
