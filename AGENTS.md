# Spice Agent Coding implementation contract

## Mission

Build the installable coding-agent distribution by composing separately owned,
versioned Spice Agent daemon and TUI contracts through a generated Spice
application. Phase 0 establishes those boundaries without inventing the APIs.

## Invariants

- Go 1.26.5 is mandatory.
- This repository owns distribution composition, configuration, packaging,
  compatibility evidence, and end-to-end acceptance—not the daemon protocol,
  terminal widget system, compiler, or framework runtime.
- The eventual executable is the generated Spice application. It must own
  daemon and terminal lifecycle, dependency construction, configuration,
  cancellation, observability, rollback, and cleanup.
- Do not present a bare daemon or TUI child process as a supported entrypoint;
  it bypasses generated ownership and can leak resources or skip policy.
- Do not create placeholder generated files. Generation begins only after the
  source annotations and adopted APIs form a valid compile-time contract.
- Commands use discrete arguments without a shell. Normal analysis is offline
  after explicit dependency preparation.
- Never commit credentials, prompts, transcripts, model output, local state,
  generated binaries, or diagnostic archives containing user data.

## Delivery

Work directly on local `main` in bounded commits. Fetch before work and again
before push. Use `make fast` and `make check` while editing; every commit must
pass `make verify`. Stop if `origin/main` moves unexpectedly.
