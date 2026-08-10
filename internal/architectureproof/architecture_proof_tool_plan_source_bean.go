package architectureproof

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/stage"
)

// NewToolPlanSource adapts the generated static dispatcher to the kernel's
// source-guaranteed immutable lease contract. Each run receives a fresh lease
// for the exact deterministic static generation.
//
// @Bean(name="architectureProofToolPlanSource")
func NewToolPlanSource(dispatcher stage.ToolDispatcher) (stage.ToolPlanSource, error) {
	source, err := stage.NewStaticToolPlanSource(dispatcher)
	if err != nil {
		return nil, err
	}
	return source, nil
}
