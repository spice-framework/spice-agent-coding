# Spice Agent OpenAI provider implementation contract

This repository owns the independently versioned OpenAI Responses integration
for Spice Agent. Work directly on local `main` in bounded commits. Fetch before
editing and immediately before pushing; never overwrite unexpected remote work.

Go 1.26.5 is mandatory. Preserve caller-owned contexts, deterministic event
translation, bounded metadata, secret redaction, conservative retry semantics,
explicit `/autoconfigure` activation, and instance ownership. Construction must
never perform network I/O or read ambient credentials. Product packages must not
import Spice compiler, command, or internal packages.

Add success and failure tests, update public documentation, run `make fast` and
`make check` during development, then run `make verify` on the exact commit tree.
Commit and push only a green tree. Never commit credentials or private release
keys, and never weaken a gate to land a change.
