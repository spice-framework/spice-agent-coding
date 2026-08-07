# Generated daemon target

`spice-agentd` is an ordinary generated Spice application. The handwritten
composition lives in `internal/daemon`; the generated, inspectable dependency
graph lives in `internal/spicegen/daemon`; and
`.spice/daemon.manifest.json` records ownership and source mappings. Generated
files and the manifest must only be changed by:

```text
go tool github.com/spice-framework/toolchain/cmd/spice generate --target Daemon . ./internal/daemon
```

The graph directly constructs typed configuration, the OpenAI provider, the
three compiled coding tools, the deterministic engine, the run host, the
authenticated gRPC server, and the local endpoint runtime. It contains no
reflection, service locator, runtime registry, or package scan.

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

This ordering is not a launcher interception mechanism. The current coding-tool
process launcher does not yet accept the adopted registry, so nested child
registration is a mandatory Phase 4 follow-up. Phase 4 containment acceptance
must not be claimed until a typed launcher SPI routes every coding-tool process
start through the registry without an ordinary `exec.Cmd.Start` registration
race.

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
