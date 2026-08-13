package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	compilerstyle "github.com/spice-framework/toolchain/compiler/style"
)

func TestStyleConfigurationOwnsExactSelectionsAndApplicationScopes(t *testing.T) {
	t.Parallel()
	root, err := (commandRunner{}).repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".spice", "style.json")
	configuration, err := compilerstyle.LoadConfiguration(path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.SchemaVersion != 2 || configuration.Profile != "java-structured" ||
		!slices.Equal(configuration.SourceRoots, []string{"cmd", "internal"}) ||
		!slices.Equal(configuration.GeneratedRoots, []string{"internal/spicegen"}) {
		t.Fatalf("style roots/profile = %#v", configuration)
	}
	falseValue := false
	wantSelections := []compilerstyle.BuildSelection{
		{Name: "darwin-amd64-default", SourceRoots: []string{"cmd", "internal"}, GOOS: "darwin", GOARCH: "amd64", CGOEnabled: &falseValue, Tags: []string{}},
		{Name: "darwin-amd64-spice-acceptance", SourceRoots: []string{"cmd", "internal"}, GOOS: "darwin", GOARCH: "amd64", CGOEnabled: &falseValue, Tags: []string{"spice_acceptance"}},
		{Name: "linux-amd64-default", SourceRoots: []string{"cmd", "internal"}, GOOS: "linux", GOARCH: "amd64", CGOEnabled: &falseValue, Tags: []string{}},
		{Name: "linux-amd64-spice-acceptance", SourceRoots: []string{"cmd", "internal"}, GOOS: "linux", GOARCH: "amd64", CGOEnabled: &falseValue, Tags: []string{"spice_acceptance"}},
		{Name: "windows-amd64-default", SourceRoots: []string{"cmd", "internal"}, GOOS: "windows", GOARCH: "amd64", CGOEnabled: &falseValue, Tags: []string{}},
		{Name: "windows-amd64-spice-acceptance", SourceRoots: []string{"cmd", "internal"}, GOOS: "windows", GOARCH: "amd64", CGOEnabled: &falseValue, Tags: []string{"spice_acceptance"}},
	}
	if !reflect.DeepEqual(configuration.BuildSelections, wantSelections) {
		t.Fatalf("build selections = %#v, want %#v", configuration.BuildSelections, wantSelections)
	}
	errorLevel := compilerstyle.RuleLevelError
	wantRules := compilerstyle.Rules{
		OnePrimaryTypePerFile: errorLevel, MethodsInPrimaryFile: errorLevel,
		FileNameMatchesType: errorLevel, PackageFunctions: errorLevel,
		ExplicitConstructors: errorLevel, ExplicitManagedScopes: errorLevel,
		BanInit: errorLevel, BanMutablePackageState: errorLevel,
		PrivateManagedFields: errorLevel, ModuleOwnership: errorLevel,
		RouteClassification: errorLevel, ContextFirst: errorLevel,
		ErrorLast: errorLevel, MaxTypeFileLines: 500,
	}
	if configuration.Rules != wantRules {
		t.Fatalf("style rules = %#v, want %#v", configuration.Rules, wantRules)
	}
	wantBoundaryFiles := []string{
		"**/*_bean.go",
		"**/*_test.go",
		"**/*_topic.go",
		"**/doc.go",
		"**/main.go",
		"**/package_constants.go",
		"cmd/*/application.go",
		"internal/architectureproof/application.go",
		"internal/spicegen/**",
	}
	wantFunctionExceptions := []compilerstyle.PackageFunctionException{
		{Glob: "**/main.go", Symbol: "main", Reason: "Go process entrypoint"},
		{Glob: "**/*_bean.go", ContributionKind: "provider", Maximum: 1, Reason: "Exact Spice provider boundary"},
		{Glob: "**/*_topic.go", ContributionKind: "event-topic", Maximum: 1, Reason: "Typed Spice event topic marker"},
		{Glob: "**/*_test.go", SymbolPattern: "^(Test|Benchmark|Fuzz|Example|TestMain)", Reason: "Go testing entrypoint"},
		{Glob: "cmd/*/application.go", ContributionKind: "application", Maximum: 1, Reason: "Compiler-validated generated application bridge"},
		{Glob: "internal/architectureproof/application.go", ContributionKind: "application", Maximum: 1, Reason: "Exact generated-package conformance application marker"},
	}
	wantVariableExceptions := []compilerstyle.PackageVariableException{
		{
			Glob: "internal/distribution/build.go", Symbol: "Commit", Type: "string",
			Reason: "Go linker -X requires the exact package variable used by release builds",
			Issue:  "CODING-LINKER-IDENTITY-001",
		},
		{
			Glob: "internal/distribution/build.go", Symbol: "Version", Type: "string",
			Reason: "Go linker -X requires the exact package variable used by release builds",
			Issue:  "CODING-LINKER-IDENTITY-001",
		},
	}
	if len(configuration.PublicRoutes) != 0 ||
		!slices.Equal(configuration.AllowedBoundaryFiles, wantBoundaryFiles) ||
		!reflect.DeepEqual(configuration.PackageFunctionExceptions, wantFunctionExceptions) ||
		!reflect.DeepEqual(configuration.PackageVariableExceptions, wantVariableExceptions) {
		t.Fatalf("style exceptions = %#v", configuration)
	}
	const applications = 3
	if got := len(configuration.BuildSelections) * applications; got != 18 {
		t.Fatalf("configured application scopes = %d, want 18", got)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.Replace(string(content), "\"schemaVersion\": 2,", "\"schemaVersion\": 2,\n  \"unknown\": true,", 1)
	if _, err = compilerstyle.DecodeConfiguration([]byte(unknown)); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown field error = %v", err)
	}
	stale := strings.Replace(string(content), "\"schemaVersion\": 2", "\"schemaVersion\": 1", 1)
	if _, err = compilerstyle.DecodeConfiguration([]byte(stale)); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("stale schema error = %v", err)
	}
}

func TestModuleDocumentsOwnExactDependencyUnion(t *testing.T) {
	t.Parallel()
	root, err := (commandRunner{}).repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	const module = "github.com/spice-framework/spice-agent-coding/"
	plain := "// @Module"
	modules := map[string]string{
		"cmd/spice-agent/doc.go":               "// @Module(allowedDependencies=[\"" + module + "internal/distribution\", \"" + module + "internal/terminal\", \"" + module + "internal/terminalcommand\", \"" + module + "internal/workspace\"])",
		"cmd/spice-agentd/doc.go":              "// @Module(allowedDependencies=[\"" + module + "internal/daemon\", \"" + module + "internal/daemoncommand\", \"" + module + "internal/distribution\", \"" + module + "internal/workspace\"])",
		"internal/architecturee2e/doc.go":      plain,
		"internal/architectureproof/doc.go":    "// @Module(allowedDependencies=[\"" + module + "internal/processplatform\", \"" + module + "internal/runidentity\", \"" + module + "internal/workspace\"])",
		"internal/daemon/doc.go":               "// @Module(allowedDependencies=[\"" + module + "internal/daemonprocess\", \"" + module + "internal/distribution\", \"" + module + "internal/processplatform\", \"" + module + "internal/runidentity\", \"" + module + "internal/workspace\"])",
		"internal/daemoncommand/doc.go":        plain,
		"internal/daemonprocess/doc.go":        "// @Module(allowedDependencies=[\"" + module + "internal/processcontainment\"])",
		"internal/devacceptance/doc.go":        plain,
		"internal/distribution/doc.go":         plain,
		"internal/installedacceptance/doc.go":  plain,
		"internal/processcontainment/doc.go":   plain,
		"internal/processplatform/doc.go":      "// @Module(allowedDependencies=[\"" + module + "internal/processcontainment\"])",
		"internal/qualitygate/doc.go":          plain,
		"internal/releaseassets/doc.go":        plain,
		"internal/releaseinstallation/doc.go":  plain,
		"internal/runidentity/doc.go":          plain,
		"internal/runtimepluginfixture/doc.go": plain,
		"internal/terminal/doc.go":             "// @Module(allowedDependencies=[\"" + module + "internal/daemonprocess\", \"" + module + "internal/distribution\", \"" + module + "internal/terminalconnector\", \"" + module + "internal/tuisession\", \"" + module + "internal/workspace\"])",
		"internal/terminalcommand/doc.go":      plain,
		"internal/terminalconnector/doc.go":    plain,
		"internal/testpath/doc.go":             plain,
		"internal/tuisession/doc.go":           plain,
		"internal/workspace/doc.go":            plain,
	}
	if len(modules) != 23 {
		t.Fatalf("module document inventory = %d, want 23", len(modules))
	}
	const annotationImport = "// @import { Module } from \"github.com/spice-framework/spice/annotation/modulith\""
	for relative, declaration := range modules {
		content, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if readErr != nil {
			t.Fatal(readErr)
		}
		text := strings.ReplaceAll(string(content), "\r\n", "\n")
		if strings.Count(text, annotationImport) != 1 || strings.Count(text, declaration) != 1 {
			t.Errorf("%s does not own exact module declaration %q", relative, declaration)
		}
		if relative == "internal/processcontainment/doc.go" && strings.Contains(text, "//go:build") {
			t.Errorf("%s must keep its module identity on every configured platform", relative)
		}
	}
}
