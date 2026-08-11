package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"runtime"

	"github.com/spice-framework/spice-agent-coding/internal/distribution"
	"github.com/spice-framework/spice-agent/client"
)

// NewServerBuild returns non-secret immutable daemon provenance.
//
// @Bean(name="serverBuild")
// @Singleton
func NewServerBuild() (client.Build, error) {
	return client.NewBuild(
		distribution.DaemonComponent,
		distribution.Version,
		distribution.Commit,
		runtime.Version(),
	)
}
