//go:build linux || darwin

package processplatform

import agentprocess "github.com/spice-framework/spice-agent/process"

type unixProcessResult struct {
	outcome    agentprocess.Outcome
	resultErr  error
	cleanupErr error
}
