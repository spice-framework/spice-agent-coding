// Command qualitygate runs the repository-owned cross-platform verification.
package main

import "os"

const (
	requiredGoVersion     = "go1.26.5"
	modulePath            = "github.com/spice-framework/spice-agent-coding"
	minimumCoverage       = 85.0
	spiceVersion          = "v0.1.0-preview.2.0.20260811041952-0e79bc4f3b29"
	toolchainVersion      = "v0.1.0-preview.2.0.20260811055955-07268323c5f9"
	agentVersion          = "v0.1.0-preview.6.0.20260811054602-8fd9ba5f8a90"
	agentTUIVersion       = "v0.1.0-preview.1"
	providerVersion       = "v0.1.0-preview.1"
	codingToolsVersion    = "v0.1.0-preview.1"
	releaseWorkflowCommit = "3bf54b986b68d386a90e418776b35ca08f234d20"
)

func main() {
	os.Exit((qualityGate{ // Entrypoint exception: propagate verification failure.
	}).execute())
}
