# Security policy

Report vulnerabilities privately through GitHub Security Advisories for this
repository. Do not include production credentials or sensitive response content
in an issue.

The provider never reads ambient OpenAI environment variables. Applications
must pass credentials through typed, secret-marked Spice configuration. Errors,
events, logs, manifests, generated files, and test fixtures must not contain API
keys, authorization headers, prompt bodies, or model output unless the owning
application explicitly chooses to retain content.

Remote API endpoints require HTTPS. Plain HTTP is accepted only for exact
`localhost` or a parsed loopback IP literal and is intended solely for trusted
local test bridges; it provides no transport confidentiality or peer
authentication. Construction performs no network I/O. Every request must use a
caller-owned context and bounded timeout. Automatic retry is limited to
failures known to happen before streaming starts; an ambiguous or partial
stream is never replayed automatically.

Supported versions receive security fixes on the latest preview line until a
stable support policy is published.

The optional live acceptance is a real billable network operation. It is
excluded from ordinary tests and requires both the `openai_live` build tag and
the exact `SPICE_OPENAI_LIVE=1` environment switch. Its failure path discards
upstream details rather than risk printing credentials. Never place a key in a
command-line argument, source file, fixture, issue, or captured test artifact.
