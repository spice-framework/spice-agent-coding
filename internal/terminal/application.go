package terminal

import (
	agenttui "github.com/spice-framework/spice-agent-tui"
	_ "github.com/spice-framework/spice-agent-tui/autoconfigure"
)

// @import { Application } from "github.com/spice-framework/spice/annotation/core"

// Terminal declares the complete generated terminal graph. Spice inspects the
// exact Shell dependency and never executes the marker body.
//
// @Application
func Terminal(agenttui.Shell) {
}
