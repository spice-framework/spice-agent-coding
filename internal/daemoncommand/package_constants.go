package daemoncommand

const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const (
	capabilityWarning = "WARNING: coding tools run with your user account's process and filesystem privileges; no sandbox or permission prompt is active.\n"
	runtimeFailure    = "spice-agentd: operation failed; see the daemon's protected diagnostics for details\n"
	invalidArguments  = "spice-agentd: invalid arguments\n"
	usage             = "Usage:\n  spice-agentd serve\n  spice-agentd --check\n  spice-agentd help\n\n" + capabilityWarning
)
