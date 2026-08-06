# Spice Agent OpenAI Provider

`spice-agent-provider-openai` is the explicit OpenAI Responses adapter for
Spice Agent. It implements the exact `model.Provider` contract, owns no global
state, and keeps model selection in each `model.Request`.

Install the provider and the exact Spice compiler tool:

```text
go get github.com/spice-framework/spice-agent-provider-openai@<version>
go get -tool github.com/spice-framework/toolchain/cmd/spice@v0.1.0-preview.1.0.20260806203056-d0b9ac086bd6
```

Construct it directly when the application wants explicit ownership:

```go
provider, err := openaiprovider.New(openaiprovider.Config{
	APIKey: os.Getenv("OPENAI_API_KEY"),
})
```

Construction validates configuration but performs no network request. The
provider never reads environment variables itself; the example deliberately
shows application-owned configuration. A request must choose its model through
`model.Request.Model`. A zero timeout selects two minutes; configured request
timeouts must be positive and no greater than thirty minutes.

Applications may opt into one replaceable fallback bean with an explicit blank
import:

```go
import _ "github.com/spice-framework/spice-agent-provider-openai/autoconfigure"
```

Importing the root package alone never activates a provider. There is no
registry, service locator, package scan, or hidden install step.

## Streaming contract

The adapter translates provider-neutral messages and function tools to the
Responses API and returns text deltas, finalized function calls, usage, and a
single completion event. Provider-hosted executable tools and unknown extension
parts fail closed. Output text and tool calls use the core operation limits.
The generated request sets `store=false`. Only a tool's model-facing name,
description, and input schema cross the provider boundary. Its effect, replay
safety, capabilities, and complete fingerprint remain host policy metadata and
are never serialized into an OpenAI request, event, diagnostic, or log.

Failures before a stream exists are `model.ProviderError`; receive failures are
`model.StreamError`. Provider retries are bounded and permitted only before the
SDK has returned a stream. Spice never replays an observed or ambiguous stream.
All public failures are redacted. One goroutine may call `Recv` while another
calls `Close`; concurrent `Recv` calls are not supported.

Successful and failed terminal events can carry a small allowlisted metadata
record. To retain it in engine observations, composition must explicitly
allowlist `openaiprovider.MetadataNamespace` in `agent.EngineOptions`. The
record is limited to response/request IDs, model, status, service tier, and HTTP
status; it cannot contain prompts, results, tool arguments, headers, tokens,
keys, URLs, or raw provider objects.

On a fresh clone, run `make tools-bootstrap` once to populate the exact product
and tools module graphs without changing tracked module files. All ordinary
quality targets remain offline. Run the offline suite with `make verify`.

## Opt-in live acceptance

The live proof is excluded from ordinary test binaries by the `openai_live`
build tag and remains disabled unless `SPICE_OPENAI_LIVE` is exactly `1`. Choose
the model explicitly so account owners retain cost and compatibility control.
For PowerShell:

```powershell
$env:SPICE_OPENAI_LIVE = "1"
$env:OPENAI_API_KEY = "<secret>"
$env:OPENAI_MODEL = "<supported-model>"
make live-acceptance
```

For Linux and macOS shells:

```text
SPICE_OPENAI_LIVE=1 OPENAI_API_KEY='<secret>' OPENAI_MODEL='<supported-model>' make live-acceptance
```

This command makes one real, billable Responses API streaming request over the
network. Pricing, rate limits, data handling, and model availability are those
of the selected OpenAI account and model. The proof sends one short text prompt,
declares no tools, sets `store=false`, disables SDK retries, uses a 90-second
client/context timeout, caps observation at 128 events and 4 KiB of text, and
requires exact `spice-live-ok` text followed by terminal completion. It never
prints the key or upstream provider details. Remove the environment variables
from the shell after the run.

See [the dependency review](docs/dependency-review.md),
[security review](docs/security-review.md), and [support matrix](docs/support.md).
