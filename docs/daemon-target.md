# Generated daemon target

`spice-agentd` is an ordinary generated Spice application. The handwritten
composition lives in `internal/daemon`; the generated, inspectable dependency
graph lives in `internal/spicegen/spice_agentd`; and
`.spice/spice_agentd.manifest.json` records ownership and source mappings. Generated
files and the manifest must only be changed by:

```text
go tool github.com/spice-framework/toolchain/cmd/spice generate --target spice-agentd ./cmd/spice-agentd
```

`cmd/spice-agentd/application.go` is the valid-Go, analysis-only package-main marker;
the normal build selects the command adapter in the same package. This makes
the target an `application-package` that `spice dev` can build and supervise
without replacing the distribution's explicit serve/check command contract.
The shipped binary and source selector are `spice-agentd`; the portable
manifest target ID and generated package suffix are `spice_agentd`. The
underscore is only compiler-owned import-path normalization and does not rename
the user-facing command.

The graph directly constructs typed configuration, the OpenAI provider, the
three compiled coding tools, the runtime-plugin Host, the deterministic engine,
the run host, the authenticated gRPC server, and the local endpoint runtime. It
contains no reflection, service locator, parallel runtime registry,
`RuntimeGraph`, or package scan.

The daemon opts in by blank-importing
`github.com/spice-framework/spice-agent/plugin/host/autoconfigure`. Spice
injects the named `read`, `replace`, and `shell` beans into an immutable
compiled dispatcher, constructs exactly one `*pluginhost.Host`, and exposes
that exact pointer as the engine's `stage.ToolPlanSource`. The daemon's existing
validated `client.Build` is explicitly adapted to the Protobuf host identity
used during a future plugin handshake. Construction does not inspect plugin
configuration, open a plugin endpoint, or launch a process; runtime activation
is an explicit Host operation and only changes immutable generations for future
runs.

## Lifecycle and publication

Construction opens protected current-user state but does not bind or publish a
daemon endpoint. `--check` therefore validates the complete graph and reverses
all generated cleanup without becoming discoverable.

`serve` binds current-user local IPC, enters the gRPC accept loop, and only then
publishes authenticated endpoint metadata. Shutdown withdraws that exact
publication before stopping admission, closing the listener, joining the serve
loop, draining the run host, canceling the daemon root, and closing protected
state. An asynchronous serve failure is surfaced to the command runner and
cannot leave the process silently alive with an unusable published endpoint.
SIGINT, SIGTERM, and EOF on managed-launch stdin all use the same bounded
generated cleanup path.

## Process containment boundary

The first generated bean adopts `daemonprocess.RootRegistry`. A malformed
managed-launch channel fails construction; explicit serve and inherited
Windows Job containment receive inert handles. Both the daemon root and coding
configuration depend on this bean, which proves adoption precedes construction
of the compiled coding tools. Generated reverse cleanup keeps the registry open
until those beans and the daemon root have been released.

The generated graph now constructs `processResolver` and `processLauncher`
beans and injects them into both the compiled shell tool and runtime-plugin
Host. Generated reverse cleanup closes the Host before the shared launcher and
root registry are released. On Windows the launcher
creates a suspended child with explicit inherited stream handles, assigns its
process tree to a kill-on-close Job, and only then resumes the child. On Unix it
starts a new process group, immediately registers the direct child with the
adopted root registry, and retains observed child identities using PID plus
process birth data. Root outcome and containment/resource completion remain
separate, so the shell tool cannot report successful cleanup while descendants
or platform ownership remain outstanding.

This is not a sandbox. Coding tools retain the user's authority. Windows Job
containment is the strong supported tree boundary. Linux and macOS have no
equivalent portable unprivileged primitive; a hostile child may fork, detach,
and disappear between process-table samples. The adapter never signals a PID or
process group after its retained birth identity no longer matches, and reports
process-table inspection failures as terminal containment failures rather than
claiming cleanup.

## Configuration

The generated schema accepts `OPENAI_API_KEY`, `OPENAI_BASE_URL`,
`OPENAI_ORGANIZATION`, `OPENAI_PROJECT`, `OPENAI_TIMEOUT`,
`OPENAI_MAX_RETRIES`, `OPENAI_MODEL`, `SPICE_AGENT_WORKSPACE`,
and `SPICE_AGENT_RUN_AUTHORITY_DIRECTORY`. The API key
is required and secret-redacted. Credentials never enter command diagnostics,
generated files, or the ownership manifest. A non-empty run-authority directory
must be absolute and satisfy the platform's current-user ownership and secure
ancestry checks; the empty default uses the protected user configuration
directory.

Use the repository checks to validate the target:

```text
make fast
make check
make verify
```
