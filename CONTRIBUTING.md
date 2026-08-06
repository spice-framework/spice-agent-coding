# Contributing

Use Go 1.26.5. Follow `ARCHITECTURE.md`; do not add speculative public APIs,
placeholder commands, or fake generated files.

Run `make fast` while editing, `make check` for the broader loop, and
`make verify` before every commit.

Dependency additions require an update to `docs/dependency-review.md` covering
maintenance, license, supply-chain risk, cancellation, observability, secrets,
and Windows/Linux behavior. Contract selection also requires executable
compatibility evidence.
