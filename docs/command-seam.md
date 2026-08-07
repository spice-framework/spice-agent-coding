# Distribution command seam

The daemon and terminal command packages establish the argument and process
boundary outside the generated product applications. They deliberately do not
construct a daemon, client, transport, or Spice application themselves.

## Commands

`internal/daemoncommand` accepts exactly:

```text
spice-agentd serve
spice-agentd --check
spice-agentd help
```

`internal/terminalcommand` accepts exactly:

```text
spice-agent
spice-agent attach --endpoint <local-endpoint>
spice-agent --check
spice-agent help
```

The terminal's empty argument list selects managed attach-or-start. The daemon
uses an explicit `serve` operation so a process never starts listening because
of an omitted argument.

Both packages parse into immutable options and invoke an injected `Runner` with
the caller-owned context. `Arguments` returns a defensive copy, and attach
preserves the endpoint byte-for-byte. Help does not invoke a runner. Exit code
zero means success, one means an execution or output failure, and two means
invalid usage.

## Security boundary

Serve, managed, and attach operations display a capability warning: coding
tools have the current user process's filesystem and process privileges, with
no sandbox or permission prompt. The command seam never includes raw argument,
endpoint, or runner error text in a diagnostic. This prevents credentials or
other sensitive values from being reflected by the outermost CLI boundary;
future protected diagnostic facilities remain responsible for detailed errors.

The attach parser accepts at most 4,096 bytes of valid UTF-8 without surrounding
whitespace or Unicode control characters. It rejects obvious network forms,
including URL schemes, host-and-port authorities, and remote UNC paths. This is
intentionally only a conservative local-only screen. It does not claim that an
opaque endpoint is a valid Unix socket or Windows named pipe. The separately
owned transport must perform authoritative operating-system validation,
current-user authorization, and connection authentication before use.

A runner failure or panic produces the same fixed failure message. Output is
also checked for errors and short writes, so the command cannot report usage or
success after failing to emit its required diagnostic or warning.

## Generated composition

The generated daemon runner owns service construction, foreground waiting, and
graceful shutdown. The generated terminal runner owns explicit attach, managed
attach-or-start, the TUI shell lifecycle, and cleanup of only a daemon candidate
it starts. `--check` constructs and reverses the same graph without publishing,
connecting, or launching a child. The command seams remain independently
unit-testable and reveal neither endpoint values nor internal failures.
