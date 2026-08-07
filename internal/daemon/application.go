package daemon

import (
	_ "github.com/spice-framework/spice-agent-provider-openai/autoconfigure"
	_ "github.com/spice-framework/spice-agent-tools-coding/autoconfigure"
	_ "github.com/spice-framework/spice-agent/plugin/host/autoconfigure"
)

// This file selects the daemon's explicit auto-configuration modules. The
// package-main @Application marker lives in cmd/spice-agentd so Spice can
// supervise the real executable package during development.
