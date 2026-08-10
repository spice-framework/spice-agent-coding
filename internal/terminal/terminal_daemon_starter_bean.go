package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"time"

	"github.com/spice-framework/spice-agent-coding/internal/daemonprocess"
)

// NewDaemonStarter selects the sibling spice-agentd executable and bounded
// candidate ownership without starting it.
//
// @Bean(name="terminalDaemonStarter")
func NewDaemonStarter() (*daemonprocess.Starter, error) {
	return daemonprocess.NewStarter(daemonprocess.Config{
		StderrBytes: 64 << 10, GracefulTimeout: 5 * time.Second,
		TerminateDelay: 2 * time.Second,
	})
}
