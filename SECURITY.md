# Security policy

Report vulnerabilities privately through GitHub Security Advisories for this
repository. Never put exploit details, credentials, prompts, transcripts, or
user data in a public issue.

The distribution will treat workspace files, daemon messages, model output,
and terminal input as untrusted. The generated application must own every
credential, process, context, policy decision, and cleanup lease. A bare child
process is not a security boundary and is not a supported launch mechanism.

Phase 0 has no runtime or third-party product dependencies. Verification tools
are isolated and pinned in `tools/go.mod`; dependency preparation is explicit,
and analysis thereafter is offline and workspace-isolated.
