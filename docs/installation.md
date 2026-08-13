# Installation

Spice Agent Coding preview archives contain two executables:

- `spice-agent` is the interactive terminal and managed local-daemon entrypoint.
- `spice-agentd` is the explicit headless daemon entrypoint.

Extract the archive for the operating system and architecture, keep the
included license, notices, and protocol descriptors alongside the binaries,
and add the extraction directory to `PATH`. A Go installation is not required
to run a released archive.

Set `OPENAI_API_KEY` and `OPENAI_MODEL`, then run:

```text
spice-agent
```

The command attaches to a compatible user-scoped daemon or starts one. To
manage the processes separately, run `spice-agentd serve`, then use the local
endpoint recorded in the current-user endpoint metadata with
`spice-agent attach --endpoint <local-endpoint>`. Run either binary with
`--check` to validate its generated graph and configuration without starting a
server or terminal session.

The architecture-proof release accepts only current-user Unix sockets on
Linux/macOS and current-user Windows named pipes. It has no remote listener.
Verify every published file against `checksums.txt` and its GitHub artifact
attestation before extracting an archive. The attestation is bound to the
exact source tag, commit, and centrally pinned distribution workflow. The
release also publishes an SPDX SBOM for independent dependency inspection.

Repository release evidence installs every Linux, macOS, and Windows archive
for both amd64 and arm64 into clean roots and compares exact contents and modes.
Only a host-compatible archive is executable; cross-architecture extraction is
structural verification, not an emulation claim. Native Windows and Linux
installed-byte performance budgets and the deterministic decisive workflow are
recorded in [Phase 6 installed release evidence](phase6-release-evidence.md).
