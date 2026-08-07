# Security policy

Report vulnerabilities privately through GitHub Security Advisories for this
repository. Do not open a public issue containing exploit details, credentials,
prompts, transcripts, or user data.

The terminal client treats all semantic, daemon, and model output as untrusted
text. Public `Text` and prompt constructors reject invalid UTF-8 and terminal
control characters, including escape sequences. View, activity, prompt, terminal
size, and rendered-frame limits bound memory and rendering work. Rendered content
must never be executed, interpolated into a shell, or persisted without an
explicit user-owned destination. Authentication material must be instance-owned,
redacted, and excluded from logs and crash reports.

Revisioned Session values defensively copy semantic values, reject stale or
malformed updates, and evict the oldest activity before item or aggregate view
bounds can be exceeded. Prompt history is memory-only, injected explicitly, and
capped at 64 entries. Each presentation effect invokes the injected Session
exactly once: it never retries an uncertain operation. Session panics become a
fixed error that does not disclose the panic value, and nested intents are
rejected. This layer does not persist, log, redact, transmit, or recover prompts;
callers must supply already-redacted semantic text.

The shell accepts its context, Session, input, output, renderer, theme, bindings,
initial view, and presentation config from its caller. Theme and binding state
is snapshotted during construction. The shell performs no network access,
module download, daemon discovery, process launch, filesystem persistence, or
global registration. Cancellation terminates active receives and performs and
is returned to the caller. The opt-in OS terminal bean binds only `os.Stdin` and
`os.Stdout`; auto-configuration deliberately contains no Session or client
configuration.

Product dependencies are pinned and vendored; their review is recorded in
`docs/dependency-review.md`. Build tools are independently pinned in
`tools/go.mod`, execute only during local verification, and receive the same
vulnerability and checksum review as product dependencies.

The annotation executable is a trusted native Go tool, not a sandbox. A
consuming application's root `go.mod` must authorize its fully qualified package
path, and standard Go module versions, checksums, vendor state, and replacements
remain visible. The tool never downloads dependencies, scans packages, executes
annotated factories, or writes generated files. It accepts only framed v1alpha2
stdio requests, validates exact descriptor/tool identities, bounds work through
caller cancellation, writes protocol frames exclusively to stdout, and reports
process diagnostics to stderr. Its handlers return generic compiler metadata;
they do not perform string-based type matching or runtime reflection.
