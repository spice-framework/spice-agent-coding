# Spice Agent Coding

This repository will provide the installable Spice Agent coding distribution.
It is currently a Phase 0 foundation: governance, ownership, compatibility
metadata, and the complete Go quality contract are implemented; runtime APIs,
commands, and generated application files are intentionally absent.

The eventual daemon and TUI are components of one inspectable generated Spice
application. Running either as a bare process would bypass generated
configuration, policy, lifecycle, rollback, telemetry, and cleanup, so it is
not a supported distribution model.

Go 1.26.5 is exact. Use `make fast`, `make check`, and `make verify`.
