package architectureproof

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"fmt"

	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
)

// NewToolDispatcher creates the immutable base dispatch surface from the
// canonical generated named-tool map.
//
// @Bean(name="architectureProofToolDispatcher")
func NewToolDispatcher(tools map[string]tool.Tool) (stage.ToolDispatcher, error) {
	dispatcher, err := stage.NewDispatcher(tools)
	if err != nil {
		return nil, fmt.Errorf("construct architecture-proof dispatcher: %w", err)
	}
	return dispatcher, nil
}
