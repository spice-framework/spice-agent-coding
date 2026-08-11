package workspace

// @import { ConfigurationProperties } from "github.com/spice-framework/spice/annotation/core"

// Properties is the shared typed workspace configuration consumed by both
// independently generated application targets.
//
// @ConfigurationProperties(prefix="agent")
type Properties struct {
	Workspace string `spice:"workspace,default=.,env=SPICE_AGENT_WORKSPACE"`
}
