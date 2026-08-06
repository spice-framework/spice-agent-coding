package agent

import (
	"context"

	"github.com/spice-framework/spice-agent/annotation/agenttool"
	"github.com/spice-framework/spice/annotation/sdk"
)

// Stage marks an exported package-level factory whose first output is one exact
// typed stage interface. The standard optional cleanup/error provider outputs
// remain valid. A typical first output is stage.Stage[Input, Output], but an application
// may use a narrower named interface. Spice derives constructor dependencies
// and output identity from the ordinary Go signature, emits a direct factory
// call, and injects ordered candidates using name, aliases, qualifiers,
// fallback, primary, and order metadata. Because the factory already returns
// its interface exactly, no InterfaceContribution or assignability guess is
// involved. The compiler supplies canonical go/types result facts; the handler
// requires a named interface origin and fails closed when facts are unavailable.
//
// The authorized native tool is selected through the consuming application's
// go.mod and runs with that user's privileges. It contributes metadata only;
// it never executes the factory or adds a runtime registry.
//
//	// @import { Stage } from "github.com/spice-framework/spice-agent/annotation/agent"
//	// @Stage(name="prompt-context", qualifiers=["default"], fallback=true, order=100)
//	func NewPromptContext(config Config) stage.Stage[Input, Output]
func Stage() sdk.Definition {
	return sdk.Definition{
		Name:    "agent.Stage",
		Summary: "Declares a named exact-interface typed pipeline stage provider.",
		Targets: []sdk.Target{sdk.TargetFunction},
		Arguments: []sdk.Argument{
			{Name: "name", Kinds: []sdk.Kind{sdk.KindString}, Description: "Required canonical static bean name.", Required: true},
			{Name: "aliases", Kinds: []sdk.Kind{sdk.KindList}, ListElementKinds: []sdk.Kind{sdk.KindString}, Description: "Optional unique canonical alternate names."},
			{Name: "qualifiers", Kinds: []sdk.Kind{sdk.KindList}, ListElementKinds: []sdk.Kind{sdk.KindString}, Description: "Optional unique canonical DI qualifiers."},
			{Name: "fallback", Kinds: []sdk.Kind{sdk.KindBoolean}, Description: "Marks an explicitly replaceable default candidate.", Default: "false"},
			{Name: "primary", Kinds: []sdk.Kind{sdk.KindBoolean}, Description: "Marks the explicitly preferred normal candidate.", Default: "false"},
			{Name: "order", Kinds: []sdk.Kind{sdk.KindInteger}, Description: "Deterministic order from -1000000 through 1000000.", Default: "0"},
		},
		Examples: []sdk.Example{{
			Title: "Fallback typed stage",
			Code:  "// @Stage(name=\"prompt-context\", fallback=true, order=100)\nfunc NewPromptContext(config Config) stage.Stage[Input, Output]",
		}},
		Compatibility: sdk.Compatibility{Since: "0.1.0-preview.1", MinimumSpice: "0.1.0-preview.1"},
		Implementation: sdk.Implementation{
			Tool:     agenttool.Path,
			Handler:  StageHandler,
			Protocol: sdk.ProtocolV1Alpha2,
		},
	}
}

// StageHandler validates compiler-owned result facts and contributes only
// generic provider and bean-selection metadata. The typed Go compiler remains
// authoritative for the stage output contract.
func StageHandler(ctx context.Context, invocation sdk.Invocation) (sdk.Result, error) {
	return providerMetadata(ctx, invocation, "Stage", factoryContract{requireName: true, result: resultStage})
}
