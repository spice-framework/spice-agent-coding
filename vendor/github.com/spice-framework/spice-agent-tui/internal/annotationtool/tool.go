// Package annotationtool implements the trusted Spice Agent TUI annotation
// tool through only the public v1alpha2 protocol.
package annotationtool

import (
	"cmp"
	"context"
	"errors"
	"runtime/debug"
	"slices"
	"strings"

	uiannotation "github.com/spice-framework/spice-agent-tui/annotation/ui"
	"github.com/spice-framework/spice-agent-tui/annotation/uitool"
	"github.com/spice-framework/spice/annotation/sdk"
	"github.com/spice-framework/spice/annotation/sdk/protocol"
)

const (
	modulePath         = "github.com/spice-framework/spice-agent-tui"
	descriptorPackage  = modulePath + "/annotation/ui"
	providerCapability = string(sdk.ContributionProvider)
	metadataCapability = string(sdk.ContributionBeanMetadata)
)

// Tool is isolated protocol dispatch state. It is not a DI registry, runtime
// graph, annotation scanner, or application extension container.
type Tool struct {
	moduleVersion string
	handlers      map[string]sdk.Handler
	descriptions  []protocol.Handler
}

// New returns an isolated tool with statically declared typed handlers.
func New() *Tool {
	registrations := handlerRegistrations()
	slices.SortFunc(registrations, func(left, right handlerRegistration) int {
		if compared := cmp.Compare(left.description.Descriptor.Package, right.description.Descriptor.Package); compared != 0 {
			return compared
		}
		return cmp.Compare(left.description.Descriptor.Name, right.description.Descriptor.Name)
	})
	result := &Tool{
		moduleVersion: selectedModuleVersion(),
		handlers:      make(map[string]sdk.Handler, len(registrations)),
		descriptions:  make([]protocol.Handler, len(registrations)),
	}
	for index, registration := range registrations {
		result.handlers[symbolKey(registration.description.Descriptor)] = registration.handle
		result.descriptions[index] = cloneDescription(registration.description)
	}
	return result
}

// Initialize validates protocol and exact executable package identity.
func (tool *Tool) Initialize(ctx context.Context, params protocol.InitializeParams) (protocol.InitializeResult, error) {
	if tool == nil {
		return protocol.InitializeResult{}, errors.New("spice agent TUI annotation tool is nil")
	}
	if err := contextError(ctx); err != nil {
		return protocol.InitializeResult{}, err
	}
	if params.Protocol != sdk.ProtocolV1Alpha2 {
		return protocol.InitializeResult{}, errors.New("spice agent TUI annotation tool requires protocol " + string(sdk.ProtocolV1Alpha2))
	}
	if params.ToolPath != uitool.Path {
		return protocol.InitializeResult{}, errors.New("spice agent TUI annotation tool path identity does not match")
	}
	return protocol.InitializeResult{
		Protocol: sdk.ProtocolV1Alpha2, ToolPath: uitool.Path,
		ModulePath: modulePath, ModuleVersion: tool.moduleVersion,
	}, nil
}

// Describe returns stable sorted descriptors and generic contribution capabilities.
func (tool *Tool) Describe(ctx context.Context, _ protocol.DescribeParams) (protocol.DescribeResult, error) {
	if tool == nil {
		return protocol.DescribeResult{}, errors.New("spice agent TUI annotation tool is nil")
	}
	if err := contextError(ctx); err != nil {
		return protocol.DescribeResult{}, err
	}
	handlers := make([]protocol.Handler, len(tool.descriptions))
	for index, description := range tool.descriptions {
		handlers[index] = cloneDescription(description)
	}
	return protocol.DescribeResult{DescriptorPackages: []string{descriptorPackage}, Handlers: handlers}, nil
}

// Analyze dispatches only descriptor identities returned by Describe.
func (tool *Tool) Analyze(ctx context.Context, params protocol.AnalyzeParams) (protocol.AnalyzeResult, error) {
	if tool == nil {
		return protocol.AnalyzeResult{}, errors.New("spice agent TUI annotation tool is nil")
	}
	if err := contextError(ctx); err != nil {
		return protocol.AnalyzeResult{}, err
	}
	if params.Descriptor.Package != params.Invocation.DescriptorPackage || params.Descriptor.Name != params.Invocation.DescriptorSymbol {
		return protocol.AnalyzeResult{}, errors.New("spice agent TUI annotation descriptor dispatch does not match invocation")
	}
	handler, found := tool.handlers[symbolKey(params.Descriptor)]
	if !found {
		return protocol.AnalyzeResult{}, errors.New("spice agent TUI annotation handler is not declared")
	}
	result, err := handler(ctx, params.Invocation)
	if err != nil {
		return protocol.AnalyzeResult{}, err
	}
	return encodeHandlerResult(result)
}

// Shutdown owns no global resources and validates cancellation consistently.
func (tool *Tool) Shutdown(ctx context.Context, _ protocol.ShutdownParams) error {
	if tool == nil {
		return errors.New("spice agent TUI annotation tool is nil")
	}
	return contextError(ctx)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("spice agent TUI annotation tool context must not be nil")
	}
	return ctx.Err()
}

func selectedModuleVersion() string {
	build, found := debug.ReadBuildInfo()
	if !found || build.Main.Path != modulePath {
		return ""
	}
	version := strings.TrimSpace(build.Main.Version)
	if version == "(devel)" {
		return ""
	}
	return version
}

type handlerRegistration struct {
	description protocol.Handler
	handle      sdk.Handler
}

func handlerRegistrations() []handlerRegistration {
	return []handlerRegistration{
		newHandlerRegistration("UIShell", uiannotation.UIShell),
		newHandlerRegistration("UIRenderer", uiannotation.UIRenderer),
	}
}

func newHandlerRegistration(symbol string, definition func() sdk.Definition) handlerRegistration {
	value := definition()
	return handlerRegistration{
		description: protocol.Handler{
			Descriptor: sdk.Symbol{Package: descriptorPackage, Name: symbol},
			Capabilities: []string{
				metadataCapability,
				providerCapability,
			},
		},
		handle: value.Implementation.Handler,
	}
}

func cloneDescription(value protocol.Handler) protocol.Handler {
	value.Capabilities = slices.Clone(value.Capabilities)
	return value
}

func symbolKey(symbol sdk.Symbol) string { return symbol.Package + "\x00" + symbol.Name }

func encodeHandlerResult(value sdk.Result) (protocol.AnalyzeResult, error) {
	result := protocol.AnalyzeResult{
		Contributions: make([]protocol.Contribution, len(value.Contributions)),
		Diagnostics:   slices.Clone(value.Diagnostics),
	}
	for index, contribution := range value.Contributions {
		encoded, err := protocol.EncodeContribution(contribution)
		if err != nil {
			return protocol.AnalyzeResult{}, err
		}
		result.Contributions[index] = encoded
	}
	return result, nil
}
