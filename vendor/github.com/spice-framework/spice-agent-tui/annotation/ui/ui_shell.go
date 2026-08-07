package ui

import (
	"context"

	"github.com/spice-framework/spice-agent-tui/annotation/uitool"
	"github.com/spice-framework/spice/annotation/sdk"
)

// UIShell marks an exported package-level factory whose first exact output is
// agenttui.Shell or a valid alias selected by the generic typed compiler. The
// factory may additionally return lifecycle.Cleanup and/or error using the
// ordinary Spice provider contract. Constructor parameters remain ordinary Go
// dependencies and generated code calls the factory directly.
//
// The required name and optional aliases, qualifiers, primary/fallback choice,
// and order are explicit static bean metadata. This annotation does not create
// a default shell, discover a daemon, infer terminal ownership, or register a
// runtime container. The trusted tool handler decodes the compiler's generic
// function-result facts and requires exact canonical Shell identity and named
// origin with interface kind. A real Go alias is accepted because its canonical
// identity is Shell; a defined wrapper, anonymous interface, or concrete result
// is rejected. Declaration.TypeID remains opaque and is never parsed. Optional
// cleanup and error results must use an ordinary supported provider shape.
//
//	// @import { UIShell } from "github.com/spice-framework/spice-agent-tui/annotation/ui"
//	// @UIShell(name="terminal", aliases=["interactive"], qualifiers=["primary-ui"], primary=true)
//	func NewTerminalShell(model Model, streams Streams) (agenttui.Shell, lifecycle.Cleanup, error)
func UIShell() sdk.Definition {
	return sdk.Definition{
		Name:    "ui.UIShell",
		Summary: "Declares an exact agenttui.Shell provider with explicit bean metadata.",
		Targets: []sdk.Target{sdk.TargetFunction},
		Arguments: []sdk.Argument{
			{Name: "name", Kinds: []sdk.Kind{sdk.KindString}, Description: "Required canonical static shell bean name.", Required: true},
			{Name: "aliases", Kinds: []sdk.Kind{sdk.KindList}, ListElementKinds: []sdk.Kind{sdk.KindString}, Description: "Optional unique canonical alternate bean names."},
			{Name: "qualifiers", Kinds: []sdk.Kind{sdk.KindList}, ListElementKinds: []sdk.Kind{sdk.KindString}, Description: "Optional unique canonical DI qualifiers."},
			{Name: "primary", Kinds: []sdk.Kind{sdk.KindBoolean}, Description: "Explicitly marks the preferred normal shell candidate.", Default: "false"},
			{Name: "fallback", Kinds: []sdk.Kind{sdk.KindBoolean}, Description: "Explicitly marks a replaceable default shell candidate.", Default: "false"},
			{Name: "order", Kinds: []sdk.Kind{sdk.KindInteger}, Description: "Deterministic order from -1000000 through 1000000.", Default: "0"},
		},
		Examples: []sdk.Example{{
			Title: "Explicit terminal shell",
			Code:  "// @UIShell(name=\"terminal\", qualifiers=[\"primary-ui\"], primary=true)\nfunc NewTerminalShell(model Model, streams Streams) agenttui.Shell",
		}},
		Compatibility: sdk.Compatibility{Since: "0.1.0-preview.1", MinimumSpice: "0.1.0-preview.1"},
		Implementation: sdk.Implementation{
			Tool:     uitool.Path,
			Handler:  UIShellHandler,
			Protocol: sdk.ProtocolV1Alpha2,
		},
	}
}

// UIShellHandler fails closed on missing or malformed compiler result facts,
// validates the exact public Shell contract, and contributes generic provider
// records without executing or parsing the annotated factory.
func UIShellHandler(ctx context.Context, invocation sdk.Invocation) (sdk.Result, error) {
	return providerMetadata(
		ctx,
		invocation,
		"UIShell",
		shellTypeID,
		shellOriginName,
	)
}
