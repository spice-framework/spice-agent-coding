package terminal

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"runtime"

	"github.com/spice-framework/spice-agent-coding/internal/distribution"
	"github.com/spice-framework/spice-agent/client"
)

// NewClientBuild contributes immutable terminal build provenance.
//
// @Bean(name="terminalClientBuild")
// @Singleton
func NewClientBuild() (client.Build, error) {
	return client.NewBuild(
		distribution.TerminalComponent,
		distribution.Version,
		distribution.Commit,
		runtime.Version(),
	)
}
