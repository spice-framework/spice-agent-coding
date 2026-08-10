package daemon

import "time"

const (
	snapshotCompatibilityIdentity          = "github.com/spice-framework/spice-agent-coding/daemon:v1"
	defaultRuntimePluginID                 = "runtime-tool"
	defaultRuntimePluginStartupTimeout     = 10 * time.Second
	defaultRuntimePluginCallTimeout        = 2 * time.Minute
	defaultRuntimePluginDrainTimeout       = 10 * time.Second
	defaultRuntimePluginShutdownTimeout    = 10 * time.Second
	defaultRuntimePluginContainmentTimeout = 5 * time.Second
	runtimePluginCleanupGrace              = time.Second
	maximumDuration                        = time.Duration(1<<63 - 1)
)
