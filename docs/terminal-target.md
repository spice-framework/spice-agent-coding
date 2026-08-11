# Generated terminal target

`cmd/spice-agent` is the package-main generation bridge for the independently
generated `spice-agent` application, not a thin
handwritten service locator. Its source composition is under
`internal/terminal`; generated direct calls and typed components are under
`internal/spicegen/spice_agent`. `.spice/spice_agent.manifest.json` records exact
ownership and maps every generated source unit back to its handwritten bean or
the TUI starter auto-configuration.

Regenerate it from its real executable package:

```text
go tool github.com/spice-framework/toolchain/cmd/spice generate --target spice-agent ./cmd/spice-agent
```

`cmd/spice-agent/application.go` is the valid-Go, analysis-only package-main
marker; normal builds select the terminal command adapter in that same
package. The resulting `application-package` layout is directly runnable by
`spice dev` while preserving managed attach-or-start behavior.
The shipped binary and source selector are `spice-agent`; the portable
manifest target ID and generated package suffix are `spice_agent`. The
underscore is only compiler-owned import-path normalization and does not rename
the user-facing command.

## Modes and ownership

The empty command selects managed attach-or-start. It first discovers protected
current-user endpoint metadata and negotiates the exact engine protocol. When no
compatible daemon exists, a current-user startup lock serializes creation of the
sibling `spice-agentd` executable. The managed connector owns and cleans up only
the candidate it started; attaching to an existing daemon never transfers
process ownership.

`spice-agent attach --endpoint <local-endpoint>` uses the caller-selected local
endpoint and never starts a daemon. Discovery authenticates with protected
endpoint metadata before gRPC initialization. Formatting, structured logging,
and JSON representations redact the endpoint.

`spice-agent --check` constructs and reverses the complete generated graph. It
does not discover an endpoint, dial gRPC, initialize a client session, start a
daemon, or enter the terminal event loop.

## Static generated graph

The graph includes typed beans for:

- terminal properties, the shared typed workspace property, and workspace
  presentation;
- current-user endpoint scope/store, managed discovery, and startup lock;
- the sibling daemon starter and managed connector;
- the exact selected `client.Connector`;
- build provenance, protocol 1.3, limits, and replay-safe initialization;
- the `coding/v1` definition and UI-neutral session adapter;
- TUI renderer, theme, key bindings, terminal I/O, configuration, and shell.

All TUI defaults come from the explicitly blank-imported
`github.com/spice-framework/spice-agent-tui/autoconfigure` starter. They remain
normal fallback beans and can be replaced through typed Spice overrides. No
runtime registry, reflection lookup, global container, or package scan exists.
The shared workspace declaration is composed as an ordinary configuration
provider; the terminal owns its resulting presentation value and no runtime
container is shared with the daemon.

## Session behavior

The terminal session performs I/O only when the shell starts it. It initializes
one negotiated client session, emits bounded monotonically revised semantic UI
updates, merges authoritative interaction snapshots/deltas, and resumes an
interrupted event stream only after its last published sequence. A sequence gap
fails closed. Submit, cancel, and interaction responses are single mutation
attempts and are never replayed after an ambiguous failure. Cleanup fences new
work and concurrently closes streams, the negotiated session, and workers.

The current TUI contract has no typed pending-interaction update, so pending
prompts are represented as terminal-safe activity text while exact correlation
remains typed inside the adapter. A future additive TUI contract can expose the
same state without changing the engine protocol.

## Verification

The repository gate runs `spice generate --check` and `--diff`, inspects bean
selection and source mappings, constructs the graph without connecting, builds
both executables with `-trimpath`, and executes shuffled, race, coverage,
security, and vendor-offline checks. Real terminal interaction on Windows and
Linux remains a release-boundary acceptance item.

Coding tools invoked by the daemon have the current user process's filesystem
and process privileges. The terminal prints the required warning; this preview
does not provide a sandbox or permission prompt.
