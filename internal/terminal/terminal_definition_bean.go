package terminal

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/client"
)

// NewDefinition selects the coding agent advertised by the generated daemon.
//
// @Bean(name="terminalDefinition")
// @Singleton
func NewDefinition() (client.DefinitionRef, error) {
	return client.NewDefinitionRef("coding", "v1")
}
