# Phase 6 Installed Release Evidence

The deterministic release-artifact gate has one unconditional Phase 6 suite
entrypoint. It binds its evidence to the candidate's
inert release metadata and an independently verified nine-subject set. It
installs all six v0.1.0-preview.5 archives twice beneath different clean
Unicode paths and requires byte-, mode-, and membership-identical trees. Only
the host-compatible archive is executed.

The native Windows/amd64 and Linux/amd64 baseline uses Go 1.26.5, exact
preview.5 bytes from release source commit
96fefab2cfcd4ff849582e0d4d328ec8c782f16d, harness source commit
b33067c66885b4e287f7ac308dc66464c6473d99, and five serial samples. Values
below are median microseconds; ceilings include observed cold-process and
mounted-filesystem variance while remaining below the owning two-minute
operation timeout.

| Operation | Windows median / ceiling | Linux median / ceiling |
| --- | ---: | ---: |
| Daemon plus required-plugin ready | 91,311 / 3,000,000 | 122,996 / 1,000,000 |
| Authenticated initialize plus Health | 2,053 / 25,000 | 1,770 / 25,000 |
| Start to first event | 526 / 10,000 | 6 / 10,000 |
| Runtime-plugin execute | 536 / 10,000 | 980 / 10,000 |
| Runtime-plugin cancel to run terminal | 524 / 10,000 | 768 / 10,000 |
| Plugin drain plus daemon shutdown | 37,435 / 250,000 | 34,608 / 250,000 |
| TUI launch to first frame | 57,560 / 2,000,000 | 24,982 / 500,000 |
| Physical and emulator resize to 100x30 | 527 / 25,000 | 991 / 25,000 |
| Daemon replacement to visible reconnect | 126,716 / 1,000,000 | 129,971 / 1,000,000 |
| ArchitectureProof generation check | 12,815,031 / 30,000,000 | 18,155,140 / 45,000,000 |
| Build both release commands | 5,305,845 / 15,000,000 | 20,334,686 / 60,000,000 |

benchmarks/installed-performance.json records every sample, exact medians,
platform evidence, and enforced ceilings. These are cross-process installed
metrics, so allocation counts are not observable. A median time increase over
20% is material and must be investigated before changing a ceiling; the
repository retains the ecosystem's 10% allocation investigation rule for
future in-process measurements.

The same suite unconditionally runs the native PTY/ConPTY daemon-replacement
proof, retaining one terminal PID and its history across a fresh daemon
identity, and then runs one decisive exact-release workflow
through authenticated IPC. A local Responses fixture drives compiled read,
replace, and shell tools, the required digest-pinned runtime plugin,
continuations, and final output. Separate runs cancel a blocked provider, a
real compiled-shell parent/child tree, and a blocked plugin call; process
witnesses, ownership-fenced reconnect, strictly increasing events,
exactly-once run terminals, privilege-warning presence, credential-canary
scans, and cleanup all fail closed.

make verify-release-live is an additional opt-in external provider proof. It is
excluded from ordinary verification and from the deterministic
release-artifact gate. It requires an independently verified artifact
directory, SPICE_DISTRIBUTION_LIVE_PROVIDER=1, OPENAI_API_KEY, and OPENAI_MODEL;
optional OpenAI endpoint, organization, and project values are passed only to
the child daemon. After shutdown it scans bounded terminal and daemon output,
endpoint metadata, and every file beneath the test-owned workspace, authority,
runtime, configuration, and home roots for the credential canary. No live
proof has been claimed without an explicit credentialed invocation.
