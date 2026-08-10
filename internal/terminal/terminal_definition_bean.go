package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/client"
)

// NewDefinition selects the coding agent advertised by the generated daemon.
//
// @Bean(name="terminalDefinition")
func NewDefinition() (client.DefinitionRef, error) {
	return client.NewDefinitionRef("coding", "v1")
}
