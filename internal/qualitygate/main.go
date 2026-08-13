package main

import "os"

const (
	requiredGoVersion     = "go1.26.5"
	modulePath            = "github.com/spice-framework/spice-agent-coding"
	minimumCoverage       = 85.0
	spiceVersion          = "v0.1.0-preview.4"
	toolchainVersion      = "v0.1.0-preview.7"
	agentVersion          = "v0.1.0-preview.7"
	agentTUIVersion       = "v0.1.0-preview.2"
	providerVersion       = "v0.1.0-preview.1"
	codingToolsVersion    = "v0.1.0-preview.1"
	releaseWorkflowCommit = "3bf54b986b68d386a90e418776b35ca08f234d20"
)

func main() {
	os.Exit((qualityGate{ // Entrypoint exception: propagate verification failure.
	}).execute())
}
