//go:build spice_generate

package main

import (
	"os"

	_ "github.com/spice-framework/spice-agent-coding/internal/devprobe"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/devprobe"
)

// @import { Application } from "github.com/spice-framework/spice/annotation/core"

// main is the valid-Go application marker for the development-loop fixture.
// Spice inspects this body but never executes it during generation.
//
// @Application
func main() {
	os.Exit(spicegen.Main(os.Args[1:]))
}
