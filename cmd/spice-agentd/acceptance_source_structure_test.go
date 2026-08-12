package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

type acceptanceSourceStructure struct{}

func TestAcceptanceConstrainedSourcesAreTypeOwned(t *testing.T) {
	t.Parallel()

	expected := map[string]map[string][]string{
		"spice-agent": {
			"acceptance_adapter_spice_acceptance.go": {"acceptanceAdapter"},
		},
		"spice-agentd": {
			"acceptance_adapter_spice_acceptance.go":                {"acceptanceAdapter"},
			"acceptance_environment_spice_acceptance.go":            {"acceptanceEnvironment"},
			"acceptance_provider_configuration_spice_acceptance.go": {"acceptanceProviderConfiguration"},
			"acceptance_provider_spice_acceptance.go":               {"AcceptanceProvider", "NewAcceptanceProvider"},
			"acceptance_scenario_spice_acceptance.go":               {"acceptanceScenario"},
			"acceptance_stream_spice_acceptance.go":                 {"acceptanceStream"},
			"blocking_acceptance_stream_spice_acceptance.go":        {"BlockingAcceptanceStream", "NewBlockingAcceptanceStream"},
			"faulting_connection_spice_acceptance.go":               {"FaultingConnection", "NewFaultingConnection"},
			"faulting_listener_factory_spice_acceptance.go":         {"faultingListenerFactory"},
			"faulting_listener_spice_acceptance.go":                 {"FaultingListener", "NewFaultingListener"},
		},
	}

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate acceptance source structure test")
	}
	commandRoot := filepath.Dir(filepath.Dir(filename))
	structure := acceptanceSourceStructure{}
	for command, files := range expected {
		directory := filepath.Join(commandRoot, command)
		retired, err := filepath.Glob(filepath.Join(directory, "*_testbuild.go"))
		if err != nil {
			t.Fatalf("discover %s retired constrained sources: %v", command, err)
		}
		if len(retired) != 0 {
			t.Fatalf("%s retains retired constrained sources: %v", command, retired)
		}
		matches, err := filepath.Glob(filepath.Join(directory, "*_spice_acceptance.go"))
		if err != nil {
			t.Fatalf("discover %s constrained sources: %v", command, err)
		}
		slices.Sort(matches)
		if len(matches) != len(files) {
			t.Fatalf("%s constrained source count = %d, want %d: %v", command, len(matches), len(files), matches)
		}
		for _, path := range matches {
			name := filepath.Base(path)
			contract, found := files[name]
			if !found {
				t.Fatalf("%s has unexpected constrained source %s", command, name)
			}
			source, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read %s/%s: %v", command, name, readErr)
			}
			if validateErr := structure.validate(name, source, contract[0], contract[1:]); validateErr != nil {
				t.Fatalf("validate %s/%s: %v", command, name, validateErr)
			}
		}
	}
}

func TestAcceptanceSourceStructureRejectsLooseDeclarations(t *testing.T) {
	t.Parallel()

	header := "//go:build spice_acceptance && !spice_generate\n\npackage main\n\n"
	tests := map[string]struct {
		source       string
		primaryType  string
		constructors []string
	}{
		"wrong build constraint": {
			source:      "//go:build spice_acceptance\n\npackage main\n\ntype sample struct{}\n",
			primaryType: "sample",
		},
		"second primary type": {
			source:      header + "type sample struct{}\ntype extra struct{}\n",
			primaryType: "sample",
		},
		"loose function": {
			source:      header + "type sample struct{}\nfunc helper() {}\n",
			primaryType: "sample",
		},
		"package variable": {
			source:      header + "type sample struct{}\nvar value any\n",
			primaryType: "sample",
		},
		"mismatched receiver": {
			source:      header + "type sample struct{}\nfunc (extra) run() {}\n",
			primaryType: "sample",
		},
		"foreign constructor": {
			source:       header + "type Sample struct{}\nfunc NewExtra() *Sample { return &Sample{} }\n",
			primaryType:  "Sample",
			constructors: []string{"NewExtra"},
		},
		"constructor returns foreign type": {
			source:       header + "type Sample struct{}\nfunc NewSample() *extra { return nil }\n",
			primaryType:  "Sample",
			constructors: []string{"NewSample"},
		},
	}
	structure := acceptanceSourceStructure{}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := structure.validate(
				"sample_spice_acceptance.go",
				[]byte(test.source),
				test.primaryType,
				test.constructors,
			); err == nil {
				t.Fatal("malformed constrained source passed structural validation")
			}
		})
	}
}

func (acceptanceSourceStructure) validate(
	name string,
	source []byte,
	primaryType string,
	constructors []string,
) error {
	const buildConstraint = "//go:build spice_acceptance && !spice_generate\n\n"
	if !strings.HasPrefix(string(source), buildConstraint) {
		return fmt.Errorf("build constraint is not exact")
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), name, source, 0)
	if err != nil {
		return fmt.Errorf("parse source: %w", err)
	}
	typeCount := 0
	for _, declaration := range parsed.Decls {
		switch current := declaration.(type) {
		case *ast.GenDecl:
			if current.Tok == token.VAR {
				return fmt.Errorf("package variable or compile assertion is forbidden")
			}
			if current.Tok != token.TYPE {
				continue
			}
			for _, specification := range current.Specs {
				typeSpecification, typeFound := specification.(*ast.TypeSpec)
				if !typeFound {
					continue
				}
				typeCount++
				if typeSpecification.Name.Name != primaryType {
					return fmt.Errorf("primary type = %s, want %s", typeSpecification.Name.Name, primaryType)
				}
			}
		case *ast.FuncDecl:
			if current.Recv == nil {
				if !slices.Contains(constructors, current.Name.Name) {
					return fmt.Errorf("loose package function %s", current.Name.Name)
				}
				if current.Name.Name != "New"+primaryType {
					return fmt.Errorf("constructor %s does not match primary type %s", current.Name.Name, primaryType)
				}
				if !(acceptanceSourceStructure{}).constructorReturns(current, primaryType) {
					return fmt.Errorf("constructor %s does not return *%s", current.Name.Name, primaryType)
				}
				continue
			}
			receiver := (acceptanceSourceStructure{}).receiverName(current.Recv.List[0].Type)
			if receiver != primaryType {
				return fmt.Errorf("method %s receiver = %s, want %s", current.Name.Name, receiver, primaryType)
			}
		}
	}
	if typeCount != 1 {
		return fmt.Errorf("primary type count = %d, want 1", typeCount)
	}
	for _, constructor := range constructors {
		found := false
		for _, declaration := range parsed.Decls {
			function, functionFound := declaration.(*ast.FuncDecl)
			if functionFound && function.Recv == nil && function.Name.Name == constructor {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("constructor %s is missing", constructor)
		}
	}
	return nil
}

func (acceptanceSourceStructure) constructorReturns(function *ast.FuncDecl, primaryType string) bool {
	if function.Type.Results == nil || len(function.Type.Results.List) != 1 {
		return false
	}
	pointer, found := function.Type.Results.List[0].Type.(*ast.StarExpr)
	if !found {
		return false
	}
	result, found := pointer.X.(*ast.Ident)
	return found && result.Name == primaryType
}

func (acceptanceSourceStructure) receiverName(expression ast.Expr) string {
	switch current := expression.(type) {
	case *ast.Ident:
		return current.Name
	case *ast.StarExpr:
		return (acceptanceSourceStructure{}).receiverName(current.X)
	default:
		return ""
	}
}
