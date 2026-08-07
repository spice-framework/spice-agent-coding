//go:build spice_generate

package main

import (
	"os"

	_ "github.com/spice-framework/spice-agent-coding/internal/daemon"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
)

// @import { Application } from "github.com/spice-framework/spice/annotation/core"

// main marks the daemon command package as one generated Spice application.
// The marker body is statically inspected and is never executed by analysis.
//
// @Application
func main() {
	os.Exit(spicegen.Main(os.Args[1:]))
}
