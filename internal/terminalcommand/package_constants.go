// Package terminalcommand defines the transport-neutral command boundary for
// the Spice Agent terminal. Client and managed-daemon behavior is injected
// through Runner and remains outside this package.
package terminalcommand

const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const (
	capabilityWarning = "WARNING: coding tools run with your user account's process and filesystem privileges; no sandbox or permission prompt is active.\n"
	runtimeFailure    = "spice-agent: operation failed; see protected diagnostics for details\n"
	invalidArguments  = "spice-agent: invalid arguments or non-local endpoint\n"
	usage             = "Usage:\n  spice-agent\n  spice-agent attach --endpoint <local-endpoint>\n  spice-agent --check\n  spice-agent help\n\n" + capabilityWarning
)
