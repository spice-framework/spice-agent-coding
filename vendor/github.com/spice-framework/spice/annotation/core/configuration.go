package core

import (
	"context"

	"github.com/spice-framework/spice/annotation/coretool"
	"github.com/spice-framework/spice/annotation/sdk"
)

// Configuration declares a constructible factory bean that owns @Bean methods.
//
// A configuration type is constructed through the same direct compile-time
// constructor selection as Service and Component. Annotated methods are then
// invoked directly on that instance. Use @ConfigurationProperties for typed
// external configuration values.
//
//	// @import { Bean, Configuration } from "github.com/spice-framework/spice/annotation/core"
//	// @Configuration
//	type DatabaseConfiguration struct{}
//
//	// @Bean
//	func (*DatabaseConfiguration) Database(properties DatabaseProperties) (*sql.DB, error)
func Configuration() sdk.Definition {
	return sdk.Definition{
		Name:    "core.Configuration",
		Summary: "Declares a constructible configuration and bean factory type.",
		Targets: []sdk.Target{sdk.TargetType},
		Arguments: []sdk.Argument{
			{
				Name:        "constructor",
				Kinds:       []sdk.Kind{sdk.KindIdentifier},
				Description: "Optional same-package constructor function.",
			},
			{
				Name:        "name",
				Kinds:       []sdk.Kind{sdk.KindString},
				Description: "Optional unique bean name.",
			},
			{
				Name:             "aliases",
				Kinds:            []sdk.Kind{sdk.KindList},
				ListElementKinds: []sdk.Kind{sdk.KindString},
				Description:      "Optional unique alternate bean names.",
			},
		},
		Examples: []sdk.Example{{
			Title: "Configuration factory",
			Code:  "// @Configuration\ntype DatabaseConfiguration struct{}\n\n// @Bean\nfunc (*DatabaseConfiguration) Database(properties DatabaseProperties) (*sql.DB, error)",
		}},
		Compatibility: sdk.Compatibility{
			Since:        "0.2.0",
			MinimumSpice: "0.2.0",
		},
		Implementation: sdk.Implementation{
			Tool:     coretool.Path,
			Handler:  ConfigurationHandler,
			Protocol: sdk.ProtocolV1Alpha2,
		},
	}
}

// ConfigurationHandler contributes configuration-factory construction metadata.
func ConfigurationHandler(
	_ context.Context,
	invocation sdk.Invocation,
) (sdk.Result, error) {
	if err := invocation.RequireDescriptor(
		"github.com/spice-framework/spice/annotation/core",
		"Configuration",
	); err != nil {
		return sdk.Result{}, err
	}
	arguments, err := sdk.BindArguments(
		invocation,
		"",
		"constructor",
		"name",
		"aliases",
	)
	if err != nil {
		return sdk.Result{}, err
	}
	constructor, err := arguments.Identifier("constructor", false)
	if err != nil {
		return sdk.Result{}, err
	}
	name, aliases, err := sdk.BeanIdentity(arguments)
	if err != nil {
		return sdk.Result{}, err
	}
	return sdk.OneContribution(sdk.Contribution{
		Kind: sdk.ContributionStereotype,
		Stereotype: &sdk.StereotypeContribution{
			Role:        "configuration",
			Construct:   true,
			Constructor: constructor,
			Name:        name,
			Aliases:     aliases,
		},
	})
}
