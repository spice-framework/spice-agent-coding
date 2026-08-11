# Security

This preview is a local coding agent with bare process privileges. Compiled
read, write, replace, and shell tools execute with the same filesystem and
process authority as the user who launches the daemon. There is no sandbox and
no default permission prompt. Both command help and first execution display
this capability warning.

Daemon transport is local-only. Linux and macOS use a current-user Unix socket;
Windows uses a current-user named pipe. Endpoint metadata and authentication
tokens are stored with user-only access, clients authenticate every connection,
and incompatible daemon versions are rejected. Remote TCP listening is not
implemented.

`OPENAI_API_KEY` is loaded through secret-redacted Spice configuration. It is
excluded from logs, events, generated Go, manifests, compatibility metadata,
and user-facing errors. Normal analysis and verification run offline; no module
or tool is downloaded in the background.

Runtime plugins are fully trusted native processes. Selection requires an
absolute executable path and matching SHA-256 digest, and every launch uses a
random handshake secret. Declared capabilities are auditable metadata, not an
enforcement boundary. Candidate processes are verified before atomic
activation, existing runs retain generation leases, and a failed candidate
cannot replace the active generation.

Review release checksums, GitHub artifact attestations, the SBOM,
`THIRD_PARTY_NOTICES.md`, and `protocol-descriptors.pb` before deployment. The
release path uses separate protected attestation and publication environments;
candidate code never receives OIDC or write authority. Do not run this preview
against an untrusted workspace or install an untrusted runtime plugin.

`make eval-opencode` is an explicit, advisory exception to normal offline
verification. It uses only integrity-pinned OpenCode bytes and exact zero-cost
OpenRouter `:free` routes, copies only the local OpenRouter credential into an
owner-only disposable data root, and runs against a sanitized disposable copy
with `--pure` and plugins, MCP, LSP, subagents, web, and shell disabled. The
harness removes its temporary home/config/repository roots and never commits raw
prompts, transcripts, model output, or credentials. See
[Advisory OpenCode evaluation](opencode-evaluation.md).
