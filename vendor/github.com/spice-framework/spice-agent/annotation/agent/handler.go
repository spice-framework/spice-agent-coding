package agent

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
	descriptorPackage       = "github.com/spice-framework/spice-agent/annotation/agent"
	modelProviderTypeID     = "github.com/spice-framework/spice-agent/model.Provider"
	modelProviderOrigin     = "github.com/spice-framework/spice-agent/model"
	toolTypeID              = "github.com/spice-framework/spice-agent/tool.Tool"
	toolOrigin              = "github.com/spice-framework/spice-agent/tool"
	stageOrigin             = "github.com/spice-framework/spice-agent/stage"
	cleanupTypeID           = "github.com/spice-framework/spice/lifecycle.Cleanup"
	cleanupOrigin           = "github.com/spice-framework/spice/lifecycle"
	maximumOrder            = int64(1_000_000)
	modelProviderOriginName = "Provider"
	toolOriginName          = "Tool"
	stageOriginName         = "Stage"
	cleanupOriginName       = "Cleanup"
)

type factoryContract struct {
	requireName bool
	result      resultContract
}

type resultContract uint8

const (
	resultStage resultContract = iota + 1
	resultTool
	resultModelProvider
)

func providerMetadata(
	ctx context.Context,
	invocation sdk.Invocation,
	symbol string,
	contract factoryContract,
) (sdk.Result, error) {
	if ctx == nil {
		return sdk.Result{}, errors.New("agent annotation handler context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return sdk.Result{}, err
	}
	if err := invocation.RequireDescriptor(descriptorPackage, symbol); err != nil {
		return sdk.Result{}, err
	}
	if err := validateFactory(invocation, contract.result); err != nil {
		return sdk.Result{}, err
	}
	arguments, err := sdk.BindArguments(
		invocation,
		"",
		"name",
		"aliases",
		"qualifiers",
		"fallback",
		"primary",
		"order",
	)
	if err != nil {
		return sdk.Result{}, err
	}
	name, aliases, err := sdk.BeanIdentity(arguments)
	if err != nil {
		return sdk.Result{}, err
	}
	if contract.requireName && name == "" {
		return sdk.Result{}, errors.New("annotation argument \"name\" is required")
	}
	if validationErr := validateIdentities(name, aliases); validationErr != nil {
		return sdk.Result{}, validationErr
	}
	qualifiers, err := arguments.Strings("qualifiers")
	if err != nil {
		return sdk.Result{}, err
	}
	if validationErr := validateIdentitySet("qualifier", qualifiers); validationErr != nil {
		return sdk.Result{}, validationErr
	}
	primary, err := arguments.Boolean("primary")
	if err != nil {
		return sdk.Result{}, err
	}
	fallback, err := arguments.Boolean("fallback")
	if err != nil {
		return sdk.Result{}, err
	}
	if primary && fallback {
		return sdk.Result{}, errors.New("agent bean cannot be both primary and fallback")
	}
	orderValue, ordered, err := boundedOrder(arguments)
	if err != nil {
		return sdk.Result{}, err
	}
	var order *int64
	if ordered {
		order = &orderValue
	}
	return sdk.Contributions(
		sdk.Contribution{
			Kind: sdk.ContributionProvider,
			Provider: &sdk.ProviderContribution{
				Name:    name,
				Aliases: aliases,
			},
		},
		sdk.Contribution{
			Kind: sdk.ContributionBeanMetadata,
			BeanMetadata: &sdk.BeanMetadataContribution{
				Qualifiers: qualifiers,
				Primary:    primary,
				Fallback:   fallback,
				Order:      order,
			},
		},
	)
}

func validateFactory(invocation sdk.Invocation, contract resultContract) error {
	declaration := invocation.Declaration
	if declaration.Target != sdk.TargetFunction {
		return errors.New("agent annotation target must be a package-level function")
	}
	if !token.IsExported(declaration.Name) {
		return errors.New("agent annotation target factory must be exported")
	}
	if strings.TrimSpace(declaration.PackagePath) == "" ||
		strings.TrimSpace(declaration.SymbolID) == "" {
		return errors.New("agent annotation target requires package and symbol identity")
	}
	if declaration.TypeID != strings.TrimSpace(declaration.TypeID) {
		return errors.New("agent annotation target type identity must be trimmed")
	}
	if receiver := invocation.Facts["receiver"]; receiver != "" {
		return errors.New("agent annotation target must not be a method")
	}
	if kind := invocation.Facts["symbol_kind"]; kind != "" && kind != "function" {
		return errors.New("agent annotation target must resolve to a function")
	}
	if !strings.HasPrefix(declaration.TypeID, "func(") {
		return errors.New("agent annotation target must have a non-generic function signature")
	}
	primary, err := primaryFunctionResult(invocation)
	if err != nil {
		return err
	}
	return validatePrimaryResult(contract, primary)
}

func primaryFunctionResult(invocation sdk.Invocation) (sdk.FunctionResultFact, error) {
	results, present, err := invocation.FunctionResultFacts()
	if err != nil {
		return sdk.FunctionResultFact{}, fmt.Errorf("decode agent factory result facts: %w", err)
	}
	if !present {
		return sdk.FunctionResultFact{}, errors.New(
			"agent annotation requires compiler function-result facts; upgrade the Spice toolchain",
		)
	}
	if len(results) == 0 || len(results) > 3 {
		return sdk.FunctionResultFact{}, errors.New(
			"agent annotation factory must return one provider value with optional lifecycle.Cleanup and error",
		)
	}
	switch len(results) {
	case 1:
		return results[0], nil
	case 2:
		if isErrorResult(results[1]) || isCleanupResult(results[1]) {
			return results[0], nil
		}
	case 3:
		if isCleanupResult(results[1]) && isErrorResult(results[2]) {
			return results[0], nil
		}
	}
	return sdk.FunctionResultFact{}, errors.New(
		"agent annotation factory results must be T, (T, error), (T, lifecycle.Cleanup), or (T, lifecycle.Cleanup, error)",
	)
}

func isErrorResult(result sdk.FunctionResultFact) bool {
	return result.CanonicalTypeID == "error" &&
		result.Kind == sdk.GoTypeInterface &&
		result.NamedOriginPackage == "" &&
		result.NamedOriginName == "error"
}

func isCleanupResult(result sdk.FunctionResultFact) bool {
	return result.CanonicalTypeID == cleanupTypeID &&
		result.Kind == sdk.GoTypeSignature &&
		result.NamedOriginPackage == cleanupOrigin &&
		result.NamedOriginName == cleanupOriginName
}

func validatePrimaryResult(
	contract resultContract,
	result sdk.FunctionResultFact,
) error {
	switch contract {
	case resultTool:
		return requireExactInterfaceResult("Tool", result, toolTypeID, toolOrigin, toolOriginName)
	case resultModelProvider:
		return requireExactInterfaceResult(
			"ModelProvider",
			result,
			modelProviderTypeID,
			modelProviderOrigin,
			modelProviderOriginName,
		)
	case resultStage:
		if result.Kind != sdk.GoTypeInterface {
			return fmt.Errorf("@Stage factory result %s must be a named Go interface", result.TypeID)
		}
		if result.NamedOriginPackage == "" || result.NamedOriginName == "" {
			return fmt.Errorf("@Stage factory result %s must have a named interface origin", result.TypeID)
		}
		if result.NamedOriginPackage == stageOrigin && result.NamedOriginName != stageOriginName {
			return fmt.Errorf("@Stage factory result %s has unsupported Spice stage origin %s.%s", result.TypeID, result.NamedOriginPackage, result.NamedOriginName)
		}
		return nil
	default:
		return errors.New("agent annotation has an unsupported result contract")
	}
}

func requireExactInterfaceResult(
	annotationName string,
	result sdk.FunctionResultFact,
	expectedTypeID string,
	expectedOriginPackage string,
	expectedOriginName string,
) error {
	if result.Kind == sdk.GoTypeInterface &&
		result.CanonicalTypeID == expectedTypeID &&
		result.NamedOriginPackage == expectedOriginPackage &&
		result.NamedOriginName == expectedOriginName {
		return nil
	}
	return fmt.Errorf(
		"@%s factory result must be exact %s (a Go alias is allowed); got %s",
		annotationName,
		expectedTypeID,
		result.TypeID,
	)
}

func validateIdentities(name string, aliases []string) error {
	if name != "" {
		if err := validateCanonicalIdentity("name", name); err != nil {
			return err
		}
	}
	if err := validateIdentitySet("alias", aliases); err != nil {
		return err
	}
	if name != "" && slices.Contains(aliases, name) {
		return fmt.Errorf("agent bean alias %q duplicates its name", name)
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
			return fmt.Errorf("agent bean %s %q is duplicated", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateCanonicalIdentity(label, value string) error {
	if len(value) == 0 || len(value) > 128 || value != strings.TrimSpace(value) {
		return fmt.Errorf("agent bean %s must be a canonical identity of 1 to 128 bytes", label)
	}
	separator := false
	for index, character := range []byte(value) {
		letter := character >= 'a' && character <= 'z'
		digit := character >= '0' && character <= '9'
		currentSeparator := character == '.' || character == '-' || character == '_'
		if index == 0 && !letter || index > 0 && !letter && !digit && !currentSeparator ||
			currentSeparator && separator {
			return fmt.Errorf("agent bean %s %q is not canonical", label, value)
		}
		separator = currentSeparator
	}
	if separator {
		return fmt.Errorf("agent bean %s %q is not canonical", label, value)
	}
	return nil
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
