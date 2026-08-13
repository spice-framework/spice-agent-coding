# Generated daemon target

The distribution pins the Spice runtime at `v0.1.0-preview.4`, Toolchain at
`v0.1.0-preview.8`, and Agent core at `v0.1.0-preview.7`. This coordinated pin supplies the
canonical `@ConfigurationProperties` contract, hyphenated property-prefix
segments, exact generated health-source injection, and plan-dependent runtime
plugin recovery policy used by this target.

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
The daemon graph explicitly composes the shared `internal/workspace` typed
property and injects it into `codingConfig`; `agent.workspace` retains its exact
default and environment mapping.

The daemon opts in by blank-importing
`github.com/spice-framework/spice-agent/plugin/host/autoconfigure`. Spice
injects the named `read`, `replace`, and `shell` beans into an immutable
compiled dispatcher, constructs exactly one `*pluginhost.Host`, and exposes
that exact pointer as the engine's `stage.ToolPlanSource`. The daemon's existing
validated `client.Build` is explicitly adapted to the Protobuf host identity
used during plugin handshakes. Construction validates at most one explicitly
configured executable but does not inspect the filesystem, open a plugin
endpoint, or launch a process. The distribution contributes the exact
`RestartPolicy` bean. It selects disabled recovery for a disabled plan and
replaces core auto-configuration's fallback with the bounded three-attempt
production policy only for an enabled plan.

Generated lifecycle invokes `RuntimePluginActivation.Start` before
`Runtime.Start`. Required activation failure returns one fixed error and stops
startup before a listener is opened or endpoint metadata is published. Optional
failure retains the compiled read, replace, and shell generation, permits local
service startup, and contributes only the fixed `dependency_degraded` health
reason. A successful activation atomically publishes the Host's immutable
runtime-tool generation for future run leases. Runtime also checks the
activation publication gate directly, so hook-order regressions fail closed.
Caller cancellation and deadlines remain discoverable through `errors.Is` and
never become availability failures. The distribution owns the exact Host bean
and gives its generated cleanup a fresh bounded context derived from the
configured drain, shutdown, and containment budgets. Consequently, cancellation
between successful plugin activation and daemon publication still contains the
child process and releases its private endpoint before launcher cleanup.

The health adapter implements the daemon's exact passive `HealthSource`
interface and samples only already-owned activation and Host state. It maps
degraded, recovering, and unavailable states to the bounded framework reason
codes and cannot expose executable paths, digests, endpoints, manifest values,
environment, stderr, or plugin-controlled text.

`testdata/runtimeplugin/go` is the independent process fixture for the current
activation contract. The repository builds it offline into a temporary directory,
pins its computed SHA-256 through `pluginhost.Executable`, and activates it with
the same contained launcher and current-user endpoint factory injected into the
daemon. Acceptance leases the resulting immutable generation, dispatches its
capability-free `fixture.echo` tool, and then requires clean Drain, Shutdown,
and process containment through the distribution activation lifecycle. The fixture is test evidence only: no executable,
digest, discovery rule, or daemon activation configuration is committed.

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
`OPENAI_MAX_RETRIES`, `OPENAI_MODEL`, `SPICE_AGENT_WORKSPACE`, and
`SPICE_AGENT_RUN_AUTHORITY_DIRECTORY`. Runtime-plugin configuration uses:

- `SPICE_AGENT_RUNTIME_PLUGIN_REQUIRED`;
- `SPICE_AGENT_RUNTIME_PLUGIN_ID`, `PATH`, `SHA256`, `MANIFEST_NAME`,
  `MANIFEST_VERSION`, and `WORKING_DIRECTORY`;
- exact capability booleans `FILESYSTEM_READ`, `FILESYSTEM_WRITE`,
  `PROCESS_EXECUTE`, `NETWORK_ACCESS`, `SECRETS_READ`, `ENVIRONMENT_READ`, and
  `ENVIRONMENT_WRITE` under the same prefix;
- bounded `STARTUP_TIMEOUT`, `CALL_TIMEOUT`, `DRAIN_TIMEOUT`,
  `SHUTDOWN_TIMEOUT`, and `CONTAINMENT_TIMEOUT` values.

The corresponding typed keys are rooted at `agent.runtime-plugin`. The complete
zero value disables plugins. Generated configuration defaults the ID to
`runtime-tool` and the startup/call/drain/shutdown/containment timeouts to
`10s`, `2m`, `10s`, `10s`, and `5s`; those defaults alone also remain disabled.
Any other nonzero value opts in and must supply a path, digest, and complete
manifest identity. The working directory deterministically defaults to the
executable's parent. Explicit paths and working directories must be absolute
and canonical, SHA-256 must be exact lowercase hexadecimal,
the child environment is always empty, protocol limits are distribution-fixed,
and enabled capabilities are emitted in canonical order. No directory scan,
manifest registry, ambient environment inheritance, or executable discovery is
performed.

The API key
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
