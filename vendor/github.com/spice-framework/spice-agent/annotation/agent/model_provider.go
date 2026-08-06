package agent

import (
	"context"

	"github.com/spice-framework/spice-agent/annotation/agenttool"
	"github.com/spice-framework/spice/annotation/sdk"
)

// ModelProvider marks an exported package-level factory whose first exact
// output is model.Provider. It may additionally return lifecycle.Cleanup and/or
// error under the standard Spice provider contract. Spice emits a direct constructor call and resolves the named
// interface bean using its canonical name, aliases, qualifiers, primary, and
// fallback metadata. No provider becomes a default implicitly: fallback or
// primary behavior exists only when the source annotation explicitly requests
// it, so multiple normal providers remain a deterministic ambiguity.
//
// An exact model.Provider return needs no InterfaceContribution and no runtime
// assignability scan. The generic compiler supplies canonical go/types result
// facts, including valid aliases; missing or non-exact facts fail closed. The
// authorized native tool is selected by go.mod, runs with user privileges, and
// returns only generic Spice contributions.
//
//	// @import { ModelProvider } from "github.com/spice-framework/spice-agent/annotation/agent"
//	// @ModelProvider(name="openai-responses", qualifiers=["reasoning"], fallback=true)
//	func NewOpenAIProvider(config Config) model.Provider
func ModelProvider() sdk.Definition {
	return sdk.Definition{
		Name:    "agent.ModelProvider",
		Summary: "Declares a canonical named exact model.Provider bean.",
		Targets: []sdk.Target{sdk.TargetFunction},
		Arguments: []sdk.Argument{
			{Name: "name", Kinds: []sdk.Kind{sdk.KindString}, Description: "Required canonical static provider bean name.", Required: true},
			{Name: "aliases", Kinds: []sdk.Kind{sdk.KindList}, ListElementKinds: []sdk.Kind{sdk.KindString}, Description: "Optional unique canonical alternate names."},
			{Name: "qualifiers", Kinds: []sdk.Kind{sdk.KindList}, ListElementKinds: []sdk.Kind{sdk.KindString}, Description: "Optional unique canonical DI qualifiers."},
			{Name: "fallback", Kinds: []sdk.Kind{sdk.KindBoolean}, Description: "Explicitly marks a replaceable default provider.", Default: "false"},
			{Name: "primary", Kinds: []sdk.Kind{sdk.KindBoolean}, Description: "Explicitly marks the preferred normal provider.", Default: "false"},
			{Name: "order", Kinds: []sdk.Kind{sdk.KindInteger}, Description: "Deterministic order from -1000000 through 1000000.", Default: "0"},
		},
		Examples: []sdk.Example{{
			Title: "Explicit fallback provider",
			Code:  "// @ModelProvider(name=\"openai-responses\", fallback=true)\nfunc NewOpenAIProvider(config Config) model.Provider",
		}},
		Compatibility: sdk.Compatibility{Since: "0.1.0-preview.1", MinimumSpice: "0.1.0-preview.1"},
		Implementation: sdk.Implementation{
			Tool:     agenttool.Path,
			Handler:  ModelProviderHandler,
			Protocol: sdk.ProtocolV1Alpha2,
		},
	}
}

// ModelProviderHandler validates provider shape and canonical compiler-owned
// result facts, then returns only generic provider and bean-selection metadata.
func ModelProviderHandler(ctx context.Context, invocation sdk.Invocation) (sdk.Result, error) {
	return providerMetadata(ctx, invocation, "ModelProvider", factoryContract{requireName: true, result: resultModelProvider})
}
