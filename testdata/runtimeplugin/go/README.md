# Distribution runtime-plugin fixture

This directory contains an independent Go executable for the distribution's
offline runtime-plugin acceptance. It imports only public Spice Agent protocol
and local-IPC packages; it does not share implementation code with the host.

The process reads one bounded private bootstrap record from standard input,
listens on that current-user local endpoint, writes the exact protocol readiness
record to standard output, and keeps standard output silent afterward. Its
authenticated manifest is `spice-agent-distribution-fixture` version `v1` and
contains one capability-free, read-only `fixture.echo` tool. Drain waits for
admitted calls and Shutdown terminates the process after the response is sent.

Repository acceptance builds the executable into a temporary directory with
the committed vendor graph, network access disabled, `-trimpath`,
`-buildvcs=false`, and an empty build ID. It computes and supplies the lowercase
SHA-256 identity to the public production Host, dispatches a progress/result
exchange, and proves authenticated drain, shutdown, process containment, and
stdout discipline. No fixture binary or digest is committed.
