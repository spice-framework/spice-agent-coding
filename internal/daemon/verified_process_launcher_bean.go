package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"errors"

	agentprocess "github.com/spice-framework/spice-agent/process"
)

// NewVerifiedProcessLauncher explicitly adapts the shared distribution
// launcher to the stronger runtime-plugin executable-lease contract.
//
// @Bean(name="verifiedProcessLauncher")
// @Singleton
func NewVerifiedProcessLauncher(
	launcher agentprocess.Launcher,
) (agentprocess.VerifiedLauncher, error) {
	verified, ok := launcher.(agentprocess.VerifiedLauncher)
	if !ok || verified == nil {
		return nil, errors.New("process launcher does not support verified executable leases")
	}
	return verified, nil
}
