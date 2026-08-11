//go:build spice_generate

package main

import (
	"os"

	_ "github.com/spice-framework/spice-agent-coding/internal/daemon"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
	_ "github.com/spice-framework/spice-agent-coding/internal/workspace"
)

// @import { Application } from "github.com/spice-framework/spice/annotation/core"
// @import { Logging } from "github.com/spice-framework/spice/annotation/observability"

// main marks the daemon command package as one generated Spice application.
// The marker body is statically inspected and is never executed by analysis.
//
// @Application
// @Logging
func main() {
	os.Exit(spicegen.Main(os.Args[1:]))
}
