package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"runtime"

	"github.com/spice-framework/spice-agent-coding/internal/distribution"
	"github.com/spice-framework/spice-agent/client"
)

// NewClientBuild contributes immutable terminal build provenance.
//
// @Bean(name="terminalClientBuild")
func NewClientBuild() (client.Build, error) {
	return client.NewBuild(
		distribution.TerminalComponent,
		distribution.Version,
		distribution.Commit,
		runtime.Version(),
	)
}
