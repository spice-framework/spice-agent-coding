package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"fmt"

	"github.com/spice-framework/spice-agent/agent"
	agentdaemon "github.com/spice-framework/spice-agent/daemon"
)

// NewDefinitionSet constructs the generated server-owned agent catalog.
//
// @Bean(name="definitionSet")
func NewDefinitionSet(properties Properties) (agentdaemon.DefinitionSet, error) {
	definition, err := agent.NewDefinition("coding", properties.Model, 32)
	if err != nil {
		return agentdaemon.DefinitionSet{}, fmt.Errorf("construct coding definition: %w", err)
	}
	value, err := agentdaemon.NewDefinition("coding", "v1", definition)
	if err != nil {
		return agentdaemon.DefinitionSet{}, err
	}
	return agentdaemon.NewDefinitionSet([]agentdaemon.Definition{value})
}
