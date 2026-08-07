package terminal

// @import { ConfigurationProperties } from "github.com/spice-framework/spice/annotation/core"

const (
	ModeManaged = "managed"
	ModeAttach  = "attach"
	ModeCheck   = "check"
)

// Properties is the complete generated terminal configuration surface.
// Mode and endpoint are injected by the command runner, not discovered through
// global mutable state.
//
// @ConfigurationProperties(prefix="agent")
type Properties struct {
	Workspace        string `spice:"workspace,default=.,env=SPICE_AGENT_WORKSPACE"`
	TerminalMode     string `spice:"terminal.mode,default=managed"`
	TerminalEndpoint string `spice:"terminal.endpoint"`
}
