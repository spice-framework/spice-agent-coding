# Architecture

## Distribution ownership

The future coding distribution is one generated Spice application:

```text
handwritten application source
        |
        v
inspectable generated application
        |-- owns daemon construction and shutdown
        |-- owns terminal-client construction and shutdown
        |-- owns configuration, policy, telemetry, rollback, and cleanup
        `-- is the only supported executable entrypoint
```

`internal/distribution` owns composition and packaging.
`internal/daemon` will adapt the separately versioned host contract.
`internal/terminal` will compose the separately versioned TUI client.

Running a daemon or terminal process directly may appear to work, but it
bypasses generated dependency, configuration, security, lifecycle, and cleanup
boundaries. That bare-process path is unsupported and must produce an explicit
warning if a future diagnostic command exposes it.

Phase 0 contains no fake application marker, generated directory, manifest, or
command. Those artifacts become legitimate only after Spice core, toolchain,
agent protocol, daemon, and TUI compatibility versions are selected and the
handwritten source exists.

## Compatibility

`compatibility.json` records Go 1.26.5 and explicit null values for contracts
that have not been adopted. Replacing null requires immutable selections and
executable compatibility tests.
