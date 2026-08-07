package ui

import (
	"context"
	"errors"
	"fmt"
	"go/token"
	"slices"
	"strings"

	"github.com/spice-framework/spice/annotation/sdk"
)

const (
	descriptorPackage  = "github.com/spice-framework/spice-agent-tui/annotation/ui"
	publicPackage      = "github.com/spice-framework/spice-agent-tui"
	shellTypeID        = publicPackage + ".Shell"
	shellOriginName    = "Shell"
	rendererTypeID     = publicPackage + ".Renderer"
	rendererOriginName = "Renderer"
	cleanupTypeID      = "github.com/spice-framework/spice/lifecycle.Cleanup"
	cleanupPackage     = "github.com/spice-framework/spice/lifecycle"
	maximumOrder       = int64(1_000_000)
)

func providerMetadata(
	ctx context.Context,
	invocation sdk.Invocation,
	symbol string,
	resultTypeID string,
	resultOriginName string,
) (sdk.Result, error) {
	if err := validateInvocation(
		ctx,
		invocation,
		symbol,
		resultTypeID,
		resultOriginName,
	); err != nil {
		return sdk.Result{}, err
	}
	arguments, err := sdk.BindArguments(invocation, "", "name", "aliases", "qualifiers", "primary", "fallback", "order")
	if err != nil {
		return sdk.Result{}, err
	}
	selection, err := bindSelection(arguments)
	if err != nil {
		return sdk.Result{}, err
	}
	return sdk.Contributions(
		sdk.Contribution{
			Kind: sdk.ContributionProvider,
			Provider: &sdk.ProviderContribution{
				Name: selection.name, Aliases: selection.aliases,
			},
		},
		sdk.Contribution{
			Kind: sdk.ContributionBeanMetadata,
			BeanMetadata: &sdk.BeanMetadataContribution{
				Qualifiers: selection.qualifiers, Primary: selection.primary,
				Fallback: selection.fallback, Order: selection.order,
			},
		},
	)
}

func validateInvocation(
	ctx context.Context,
	invocation sdk.Invocation,
	symbol string,
	resultTypeID string,
	resultOriginName string,
) error {
	if ctx == nil {
		return errors.New("TUI annotation handler context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := invocation.RequireDescriptor(descriptorPackage, symbol); err != nil {
		return err
	}
	if err := validateFactory(invocation); err != nil {
		return err
	}
	return validateProviderResult(invocation, resultTypeID, resultOriginName)
}

func validateProviderResult(
	invocation sdk.Invocation,
	expectedTypeID string,
	expectedOriginName string,
) error {
	results, present, err := invocation.FunctionResultFacts()
	if err != nil {
		return fmt.Errorf("decode TUI factory result facts: %w", err)
	}
	if !present {
		return errors.New("TUI factory requires generic function result facts from the Spice toolchain")
	}
	providerResult, err := primaryFunctionResult(results)
	if err != nil {
		return err
	}
	if providerResult.Kind != sdk.GoTypeInterface {
		return fmt.Errorf(
			"TUI factory result zero must have effective Go kind interface, got %s",
			providerResult.Kind,
		)
	}
	if providerResult.CanonicalTypeID != expectedTypeID {
		return fmt.Errorf(
			"TUI factory result zero must have exact canonical type %s, got %s",
			expectedTypeID,
			providerResult.CanonicalTypeID,
		)
	}
	if providerResult.NamedOriginPackage != publicPackage ||
		providerResult.NamedOriginName != expectedOriginName {
		return fmt.Errorf(
			"TUI factory result zero must originate from %s.%s, got %s.%s",
			publicPackage,
			expectedOriginName,
			providerResult.NamedOriginPackage,
			providerResult.NamedOriginName,
		)
	}
	return nil
}

func primaryFunctionResult(
	results []sdk.FunctionResultFact,
) (sdk.FunctionResultFact, error) {
	if len(results) == 0 || len(results) > 3 {
		return sdk.FunctionResultFact{}, errors.New(
			"TUI factory must return one provider value with optional lifecycle.Cleanup and error",
		)
	}
	switch len(results) {
	case 1:
		return results[0], nil
	case 2:
		if isCleanupResult(results[1]) || isErrorResult(results[1]) {
			return results[0], nil
		}
	case 3:
		if isCleanupResult(results[1]) && isErrorResult(results[2]) {
			return results[0], nil
		}
	}
	return sdk.FunctionResultFact{}, errors.New(
		"TUI factory results must be T, (T, error), (T, lifecycle.Cleanup), or (T, lifecycle.Cleanup, error)",
	)
}

func isCleanupResult(result sdk.FunctionResultFact) bool {
	return result.CanonicalTypeID == cleanupTypeID &&
		result.Kind == sdk.GoTypeSignature &&
		result.NamedOriginPackage == cleanupPackage &&
		result.NamedOriginName == "Cleanup"
}

func isErrorResult(result sdk.FunctionResultFact) bool {
	return result.CanonicalTypeID == "error" &&
		result.Kind == sdk.GoTypeInterface &&
		result.NamedOriginPackage == "" &&
		result.NamedOriginName == "error"
}

type beanSelection struct {
	name       string
	aliases    []string
	qualifiers []string
	primary    bool
	fallback   bool
	order      *int64
}

func bindSelection(arguments sdk.BoundArguments) (beanSelection, error) {
	name, aliases, err := sdk.BeanIdentity(arguments)
	if err != nil {
		return beanSelection{}, err
	}
	if name == "" {
		return beanSelection{}, errors.New("annotation argument \"name\" is required")
	}
	if validationErr := validateIdentities(name, aliases); validationErr != nil {
		return beanSelection{}, validationErr
	}
	qualifiers, err := arguments.Strings("qualifiers")
	if err != nil {
		return beanSelection{}, err
	}
	if validationErr := validateIdentitySet("qualifier", qualifiers); validationErr != nil {
		return beanSelection{}, validationErr
	}
	primary, err := arguments.Boolean("primary")
	if err != nil {
		return beanSelection{}, err
	}
	fallback, err := arguments.Boolean("fallback")
	if err != nil {
		return beanSelection{}, err
	}
	if primary && fallback {
		return beanSelection{}, errors.New("TUI bean cannot be both primary and fallback")
	}
	orderValue, ordered, err := boundedOrder(arguments)
	if err != nil {
		return beanSelection{}, err
	}
	var order *int64
	if ordered {
		order = &orderValue
	}
	return beanSelection{
		name: name, aliases: aliases, qualifiers: qualifiers,
		primary: primary, fallback: fallback, order: order,
	}, nil
}

func validateFactory(invocation sdk.Invocation) error {
	declaration := invocation.Declaration
	if declaration.Target != sdk.TargetFunction {
		return errors.New("TUI annotation target must be a package-level function")
	}
	if !token.IsExported(declaration.Name) {
		return errors.New("TUI annotation target factory must be exported")
	}
	if strings.TrimSpace(declaration.PackagePath) == "" || strings.TrimSpace(declaration.SymbolID) == "" {
		return errors.New("TUI annotation target requires package and symbol identity")
	}
	if invocation.Facts["receiver"] != "" {
		return errors.New("TUI annotation target must not be a method")
	}
	if kind := invocation.Facts["symbol_kind"]; kind != "" && kind != "function" {
		return errors.New("TUI annotation target must resolve to a function")
	}
	return nil
}

func validateIdentities(name string, aliases []string) error {
	if err := validateCanonicalIdentity("name", name); err != nil {
		return err
	}
	if err := validateIdentitySet("alias", aliases); err != nil {
		return err
	}
	if slices.Contains(aliases, name) {
		return fmt.Errorf("TUI bean alias %q duplicates its name", name)
	}
	return nil
}

func validateIdentitySet(label string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateCanonicalIdentity(label, value); err != nil {
			return err
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("TUI bean %s %q is duplicated", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateCanonicalIdentity(label, value string) error {
	if len(value) == 0 || len(value) > 128 || value != strings.TrimSpace(value) {
		return fmt.Errorf("TUI bean %s must be a canonical identity of 1 to 128 bytes", label)
	}
	separator := false
	for index, character := range []byte(value) {
		valid, currentSeparator := validIdentityByte(index, character, separator)
		if !valid {
			return fmt.Errorf("TUI bean %s %q is not canonical", label, value)
		}
		separator = currentSeparator
	}
	if separator {
		return fmt.Errorf("TUI bean %s %q is not canonical", label, value)
	}
	return nil
}

func validIdentityByte(index int, character byte, previousSeparator bool) (valid, separator bool) {
	letter := character >= 'a' && character <= 'z'
	if index == 0 {
		return letter, false
	}
	if letter || character >= '0' && character <= '9' {
		return true, false
	}
	separator = character == '.' || character == '-' || character == '_'
	return separator && !previousSeparator, separator
}

func boundedOrder(arguments sdk.BoundArguments) (int64, bool, error) {
	if _, present := arguments["order"]; !present {
		return 0, false, nil
	}
	value, err := arguments.Integer("order")
	if err != nil {
		return 0, false, err
	}
	if value < -maximumOrder || value > maximumOrder {
		return 0, false, fmt.Errorf("annotation argument \"order\" must be between %d and %d", -maximumOrder, maximumOrder)
	}
	return value, true, nil
}
