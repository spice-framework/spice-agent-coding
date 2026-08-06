# Architecture

This module is an isolated adapter from OpenAI's Responses API to the public
Spice Agent model contract.

The root package owns validated configuration and an instance-scoped Responses
service. `Client.Stream` performs a deterministic, bounded translation from a
validated `model.Request`; the request's model is authoritative. The adapter
prefetches the first SDK event before returning a stream so startup failures can
be represented as `model.ProviderError`. One bounded pump then owns the raw SDK
stream, allowing each `Recv` call to honor its own context without leaking one
goroutine per deadline. `Close` may race with the sole `Recv` caller, cancels
the operation, and joins that pump. Multiple concurrent `Recv` calls are not
supported.

Only finalized function calls from the completed response become Spice tool
calls. This prevents partial argument deltas from becoming executable work.
Unknown tools, provider-hosted executable output, malformed arguments,
duplicate call IDs, inconsistent final text, and limit violations fail closed.
The adapter does not expose provider reasoning or raw SDK objects.

`autoconfigure` is the only library-default activation surface. It is selected
solely by an explicit blank import and contributes one fallback
`model.Provider`; an application-owned provider replaces it through ordinary
Spice DI. The root package has no activation side effect.

The adapter may depend on `spice-agent` and `openai-go`; neither dependency may
depend back on this repository. It does not own an agent loop, dispatcher,
daemon, registry, service locator, or package scanner. Callers own contexts and
engine metadata allowlists. No retry occurs after a stream has been returned.

An application-supplied `http.Client` and transport are trusted. Spice supplies
the bounded request context and calls stream close, but cannot safely force-stop
a transport that ignores both cancellation and close. Such a transport may
prevent `Close` from joining and is outside this module's cooperative contract.
