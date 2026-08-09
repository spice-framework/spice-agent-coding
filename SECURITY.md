# Security policy

Report vulnerabilities privately through GitHub Security Advisories for this
repository. Never put exploit details, credentials, prompts, transcripts, or
user data in a public issue.

The distribution will treat workspace files, daemon messages, model output,
and terminal input as untrusted. Each generated application must own every
credential, context, policy decision, and cleanup lease in its boundary.
`spice-agentd serve`, `spice-agent attach`, and managed attach-or-start are
supported lifecycle modes. Managed launch owns and cleans up only the daemon it
starts; attaching does not transfer daemon ownership.

Coding tools execute with the operating-system user's privileges in the process
that dispatches them and are not sandboxed. "Bare privileges" describes that
authority; the generated daemon's direct serve mode remains supported. Process
separation alone is not a security boundary. Windows process trees are owned by
kill-on-close Jobs before execution begins. Unix ownership combines process
groups and observed PID/birth identities, but cannot guarantee observation of
an adversarial fork-and-detach between process-table samples.

Runtime dependencies and their exact compatible versions are declared in
`go.mod`, verified by `go.sum`, and committed in `vendor`. Verification tools
are isolated and pinned in `tools/go.mod`; dependency preparation is explicit,
and analysis thereafter is offline and workspace-isolated.

Distribution releases use no long-lived signing key. Candidate validation is
uncredentialed, deterministic construction is owned by the pinned development
renderer, and a separately pinned toolchain verifier authenticates the exact
files before they enter protected keyless attestation and publication jobs.
Only GitHub OIDC provenance from the exact organization workflow, source tag,
and commit is accepted.
