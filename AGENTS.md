# Spice Agent Coding implementation contract

## Mission

Build the installable coding-agent distribution by composing separately owned,
versioned Spice Agent daemon and TUI contracts through two independently
generated Spice applications. The repository foundation establishes those
boundaries without inventing the APIs.

## Invariants

- Go 1.26.5 is mandatory.
- This repository owns distribution composition, configuration, packaging,
  compatibility evidence, and end-to-end acceptance—not the daemon protocol,
  terminal widget system, compiler, or framework runtime.
- `spice-agentd` is one generated Spice `@Application` target; `spice-agent` is
  the independently generated TUI and managed-launcher `@Application` target.
- Explicit daemon serve and client attach are supported. Managed launch must
  attach to a compatible daemon or start one and must clean up only a daemon
  process whose lifecycle it owns.
- The statement that coding tools have bare user-process privileges describes
  their security authority. A directly launched generated daemon is supported;
  daemon and TUI lifecycle do not collapse into one process.
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
