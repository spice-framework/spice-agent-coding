// Package managed coordinates attach-or-start behavior while retaining exact
// ownership of only the daemon candidate it launches. Process creation,
// endpoint discovery, and transport implementations remain injected.
//
// A Connector starts a daemon only when Discovery returns ErrEndpointNotFound.
// Existing endpoints, including incompatible or unauthenticated endpoints, are
// never killed, replaced, or silently bypassed. Startup is serialized by a
// caller-supplied current-user lock and bounded by both the operation context
// and the configured timeout. Failed or canceled launches are shut down and
// joined; Shutdown never targets a daemon found through Discovery. A
// candidate's process result is separate from its containment join: ownership
// is released only after Wait confirms that every owned resource is safe. A
// retryable or unclassified join failure permits a later proof attempt. An
// explicitly non-retryable join failure is retained for manual recovery and is
// returned by later Shutdown calls without repeating unsafe cleanup actions.
package managed
