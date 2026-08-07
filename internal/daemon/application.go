package daemon

import (
	_ "github.com/spice-framework/spice-agent-provider-openai/autoconfigure"
	_ "github.com/spice-framework/spice-agent-tools-coding/autoconfigure"
	_ "github.com/spice-framework/spice-agent/plugin/host/autoconfigure"
)

// @import { Application } from "github.com/spice-framework/spice/annotation/core"

// Daemon declares the complete daemon graph. Spice analyzes the exact
// Runtime dependency and never executes this body during generation.
//
// @Application
func Daemon(*Runtime) {
}
