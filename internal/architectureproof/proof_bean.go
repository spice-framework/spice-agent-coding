package architectureproof

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"fmt"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/stage"
)

// NewProof consumes the exact provider and canonical named tool map selected
// by Spice. It does not discover or register implementations at runtime.
//
// @Bean(name="proof")
// @Singleton
func NewProof(
	engine *agent.Engine,
	dispatcher stage.ToolDispatcher,
	fixture *ResponsesFixture,
) (*Proof, error) {
	if engine == nil {
		return nil, fmt.Errorf("construct architecture proof: engine is nil")
	}
	if fixture == nil {
		return nil, fmt.Errorf("construct architecture proof: responses fixture is nil")
	}
	if dispatcher == nil {
		return nil, fmt.Errorf("construct architecture proof: tool dispatcher is nil")
	}
	names := make([]string, 0, len(dispatcher.Definitions()))
	for _, definition := range dispatcher.Definitions() {
		names = append(names, definition.Name())
	}
	return &Proof{engine: engine, fixture: fixture, tools: names}, nil
}
