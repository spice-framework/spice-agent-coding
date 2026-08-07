# Spice Agent TUI implementation contract

## Mission

Build the standalone terminal experience for Spice Agent around a minimal
UI-facing Session SPI. The high-level client implementation, transport,
discovery, reconnect, and daemon lifecycle remain owned outside this module.

## Invariants

- Go 1.26.5 is mandatory.
- The Bubble Tea v2 shell, renderers, editor, commands, keybindings, terminal
  presentation, input handling, and client application composition belong here.
  Daemon behavior, coding-agent policy, compiler behavior, protocol ownership,
  gRPC, and operating-system IPC do not.
- Keep the public Session contract limited to UI-neutral Receive and Perform
  operations. Do not add a private transport, protocol, discovery, or daemon
  API.
- Do not add the Bubble Tea v2 dependency merely as a placeholder. Pin it only
  with real, tested presentation code and a recorded dependency review.
- Commands use discrete arguments without a shell. `make tools-bootstrap` is
  the sole explicit network-enabled dependency preparation; ordinary
  verification is offline and workspace-isolated.
- Do not commit credentials, transcripts, prompts, model output, local state,
  generated binaries, or terminal recordings containing user data.

## Delivery

Work directly on local `main` in bounded commits. Fetch before work and again
before push. Every commit must pass `make verify`; use `make fast` and
`make check` for shorter feedback. Stop if `origin/main` moves unexpectedly.
