package architectureproof

import (
	_ "github.com/spice-framework/spice-agent-provider-openai/autoconfigure"
	_ "github.com/spice-framework/spice-agent-tools-coding/autoconfigure"
)

// @import { Application } from "github.com/spice-framework/spice/annotation/core"

// ArchitectureProof is compile-time metadata for the generated SDK proof.
// Spice analyzes its exact parameter type and never executes this body.
//
// @Application
func ArchitectureProof(*Proof) {
}
