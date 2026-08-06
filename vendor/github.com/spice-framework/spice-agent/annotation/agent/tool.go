package agent

import (
	"context"

	"github.com/spice-framework/spice-agent/annotation/agenttool"
	"github.com/spice-framework/spice/annotation/sdk"
)

// Tool marks an exported package-level factory whose first exact output is
// tool.Tool. It may additionally return lifecycle.Cleanup and/or error under
// the standard Spice provider contract. Spice derives dependencies from the Go parameters, emits an
// ordinary direct call, and supplies named candidates to map[string]tool.Tool.
// The required canonical name is the static DI/map identity; model-provided
// input can never select an undeclared bean. Aliases, qualifiers, replacement,
// preference, and ordering use normal generated Spice bean selection.
// Each implementation's tool.Definition must explicitly classify its external
// effect and replay safety and declare a canonical capability set. Dispatcher
// construction fails closed on missing, contradictory, or invalid metadata.
//
// An exact interface-returning factory needs no InterfaceContribution: its Go
// output already is tool.Tool. The generic compiler derives canonical result
// facts with go/types, including valid aliases, and the handler rejects missing
// or non-exact facts. Display strings never determine assignability. The
// authorized tool process contributes generic provider/metadata records only
// and has the caller's native process privileges.
//
//	// @import { Tool } from "github.com/spice-framework/spice-agent/annotation/agent"
//	// @Tool(name="read", qualifiers=["coding"], order=10)
//	func NewReadTool(files Filesystem) tool.Tool
func Tool() sdk.Definition {
	return sdk.Definition{
		Name:    "agent.Tool",
		Summary: "Declares a canonical named exact tool.Tool provider.",
		Targets: []sdk.Target{sdk.TargetFunction},
		Arguments: []sdk.Argument{
			{Name: "name", Kinds: []sdk.Kind{sdk.KindString}, Description: "Required canonical static bean and tool-map name.", Required: true},
			{Name: "aliases", Kinds: []sdk.Kind{sdk.KindList}, ListElementKinds: []sdk.Kind{sdk.KindString}, Description: "Optional unique canonical alternate names."},
			{Name: "qualifiers", Kinds: []sdk.Kind{sdk.KindList}, ListElementKinds: []sdk.Kind{sdk.KindString}, Description: "Optional unique canonical DI qualifiers."},
			{Name: "fallback", Kinds: []sdk.Kind{sdk.KindBoolean}, Description: "Marks an explicitly replaceable default candidate.", Default: "false"},
			{Name: "primary", Kinds: []sdk.Kind{sdk.KindBoolean}, Description: "Marks the explicitly preferred normal candidate.", Default: "false"},
			{Name: "order", Kinds: []sdk.Kind{sdk.KindInteger}, Description: "Deterministic order from -1000000 through 1000000.", Default: "0"},
		},
		Examples: []sdk.Example{{
			Title: "Named coding tool",
			Code:  "// @Tool(name=\"read\", qualifiers=[\"coding\"])\nfunc NewReadTool(files Filesystem) tool.Tool",
		}},
		Compatibility: sdk.Compatibility{Since: "0.1.0-preview.1", MinimumSpice: "0.1.0-preview.1"},
		Implementation: sdk.Implementation{
			Tool:     agenttool.Path,
			Handler:  ToolHandler,
			Protocol: sdk.ProtocolV1Alpha2,
		},
	}
}

// ToolHandler validates provider shape and canonical compiler-owned result facts,
// then contributes only generic provider and bean-selection metadata.
func ToolHandler(ctx context.Context, invocation sdk.Invocation) (sdk.Result, error) {
	return providerMetadata(ctx, invocation, "Tool", factoryContract{requireName: true, result: resultTool})
}
