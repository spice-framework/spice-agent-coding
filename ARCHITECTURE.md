# Architecture

## Distribution ownership

The coding distribution contains two independently generated Spice application
targets:

```text
spice-agentd @Application             spice-agent @Application
        |                                      |
        v                                      v
inspectable generated daemon          inspectable generated TUI/launcher
        |                                      |
        |-- authenticated local IPC            |-- explicit attach
        |-- engine and run lifecycle            |-- managed attach-or-start
        |-- configuration and policy            |-- terminal lifecycle
        `-- graceful shutdown                   `-- owned-child cleanup
```

`spice-agentd` supports explicit serve. `spice-agent` supports explicit attach;
its default managed mode attaches to a compatible daemon or starts one. An
attached daemon remains externally owned. A daemon started by managed mode is a
supported process whose lease and cleanup belong to that launcher instance.
Each target independently owns dependency construction, configuration,
cancellation, observability, rollback, and cleanup for its boundary.

`internal/distribution` owns composition and packaging.
`internal/daemon` will adapt the separately versioned host contract.
`internal/terminal` will compose the separately versioned TUI client.

Explicit serve and attach are supported lifecycle modes. Internal packages are
not alternative entrypoints, and neither generated target may bypass the other
target's protocol boundary. The security phrase "bare user-process privileges"
applies to coding tools: they execute with the selected user process's authority
and are not sandboxed. It does not describe or prohibit the daemon process
topology.

Phase 0 contains no fake application marker, generated directory, manifest, or
command. Those artifacts become legitimate only after Spice core, toolchain,
agent protocol, daemon, and TUI compatibility versions are selected and the
handwritten source exists.

## Compatibility

`compatibility.json` records Go 1.26.5 and explicit null values for every
distribution module that has not been adopted: Spice, the Spice toolchain,
Spice Agent, the TUI, the OpenAI provider, and coding tools. Replacing null
requires immutable selections and executable compatibility tests.
