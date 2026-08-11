# Advisory OpenCode evaluation

`make eval-opencode` runs a bounded, networked evaluation of the coding-agent
workspace with exact free OpenRouter routes. It is an advisory developer tool,
not a release or verification gate. The ordinary `make fast`, `make check`, and
`make verify` paths remain offline and never invoke a model.

## Pinned evaluator

The harness downloads `opencode-ai@1.18.16` directly from the npm registry,
verifies the root package and selected native package before extraction, and
runs only the verified native executable. It does not run npm lifecycle scripts.

| package | SHA-512 integrity |
| --- | --- |
| `opencode-ai@1.18.16` | `sha512-l4nUfoucuw8u5WYU9my9Yz7lYpBI649i/ppgL0BGTjp/HC3p2jN50i331YpcGbKfGTEv9VG6mxU1+QZyaR5hxA==` |
| `opencode-windows-x64-baseline@1.18.16` | `sha512-5ZnpdRq4KICElnb/OQ1PtufgmcxAYLILEJiu9rKJjAxTYjFEWMkpA6SRiU4LRbYzQn8LaHObDgxzt7bquA0OTw==` |
| `opencode-windows-arm64@1.18.16` | `sha512-FZrB40RBm5gvv3Uv+WOSRlEHQsqcJ04t7B3yp/L6SFYU6T2UZQqvLwDF/TPT1C0//a8uAbfqUV1h26sTmsi4ow==` |
| `opencode-linux-x64-baseline@1.18.16` | `sha512-Lvm4XLm918etLz85Yh8CCTcCalLUjx3TA8KVq3S4+EfTNBJ3QOmUyLjGQPhuC2kw+5NvkQVV/mnVdCawxnJ6ng==` |
| `opencode-linux-arm64@1.18.16` | `sha512-0s32hDy72CBsT6sK7xsDUNKrACmylz5TIADHcYf8BXm7cHA/ry6fVNZ6r/RDtdQxRv6Hr47bynx+NJ8rm9SZAA==` |
| `opencode-darwin-x64-baseline@1.18.16` | `sha512-IxX00YOhWQ38f54ZR+g9bJTtRK7cUCKM7VzGaHbOgk8sfqAxNUJEhz1+BY/V0eODE76jh8lKM5Bjm/vqBno92Q==` |
| `opencode-darwin-arm64@1.18.16` | `sha512-/eEAcBOMOAv2c35s+1smy8+8VxGHOAbH8bIgcYdJJ8rJNMRMtSrdhsHFKsa/27oYbv1k/WHHU8XYddLvCoCXVw==` |

Before any authenticated request, the harness checks OpenRouter's public model
catalog and requires each configured route to advertise zero prompt and
completion prices plus tool support. The only accepted model identifiers are:

- `openrouter/openai/gpt-oss-20b:free`
- `openrouter/cohere/north-mini-code:free`
- `openrouter/nvidia/nemotron-3-super-120b-a12b:free`

The `:free` suffix is part of the required identifier. Fallbacks are disabled;
the harness rejects any model or price drift instead of selecting another route.

## Isolation and permissions

The source worktree must be clean. The harness copies only tracked regular files
into a disposable repository without `.git`, rejects links, unsafe credential
paths, special files, and oversized inputs, and records the complete tree digest
before and after each case. It creates disposable `HOME`, `USERPROFILE`, XDG,
application-data, cache, state, and temporary directories and supplies a minimal
child environment. Existing OpenCode configuration is not inherited.

If a local OpenCode credential store contains an OpenRouter credential, the
harness decodes only that provider entry and writes it with owner-only access
inside the disposable data directory. The credential value is never displayed,
placed on a command line, copied into the repository, or retained as evidence.
Absence of the credential is reported as an infrastructure failure.

OpenCode always runs with `--pure`. The generated configuration has no plugins,
MCP servers, LSP servers, formatters, instructions, web tools, skills, task tools,
shell, or subagents. Provider and model whitelists, deny-by-default permissions,
step limits, and zero subagent depth are validated through OpenCode's resolved
configuration before a model is invoked.

## Cases and scoring

Every allowlisted model receives two deterministic cases:

1. The read-only audit permits only bounded repository reads. It must inspect the
   workspace, report the required completion marker, use a repository read tool,
   and leave the complete tree digest unchanged.
2. The disposable seeded-defect case changes one boundary comparison in a copied
   parser. The model may read and edit only inside the copy, must restore the
   pristine complete-tree digest, may change no other path, must report the
   required completion marker, and must pass the focused offline Go test.

The local harness, not the model, applies the rubric. Each invocation has a two
minute timeout, fixed step and tool-call ceilings, a 2,048-token output ceiling,
bounded event and diagnostic capture, and a whole-run deadline. Any positive
reported cost is a safety failure and cancels the run. Tree digests, exact change
sets, completion markers, and the focused test determine pass or fail.

Console output contains only aggregate case status, classification, duration,
reported cost, bounded counts, and short digests. Raw prompts, transcripts,
model text, and temporary repositories are not written to tracked files. Rate
limits and authentication, model availability, timeout, download, or service
failures are classified separately from rubric failures so advisory model
availability cannot be mistaken for a product defect.
