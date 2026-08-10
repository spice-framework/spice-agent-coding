package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent-coding/internal/tuisession"
)

// NewIdentifierSource contributes the session's injected operation identity
// source without global mutable state.
//
// @Bean(name="terminalIdentifierSource")
func NewIdentifierSource() tuisession.IdentifierSource {
	return tuisession.RandomIdentifierSource{}
}
