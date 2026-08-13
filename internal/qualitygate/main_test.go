package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestQualityGateTimeoutsCoverEveryModeAndUnknown(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		mode string
		want time.Duration
	}{
		{mode: "tools-bootstrap", want: 15 * time.Minute},
		{mode: "fast", want: 15 * time.Minute},
		{mode: "check", want: 15 * time.Minute},
		{mode: "coverage", want: 15 * time.Minute},
		{mode: "fmt", want: 15 * time.Minute},
		{mode: "verify", want: 30 * time.Minute},
		{mode: "release-artifacts", want: 15 * time.Minute},
		{mode: "opencode-eval", want: maximumOpenCodeEvaluationDuration + time.Minute},
		{mode: "", want: 15 * time.Minute},
		{mode: "unknown", want: 15 * time.Minute},
	} {
		if got := (qualityGate{}).qualityGateTimeout(test.mode); got != test.want {
			t.Errorf("qualityGateTimeout(%q) = %s, want %s", test.mode, got, test.want)
		}
	}
}

func TestQualityGateContextPreservesDeadlineAndCancellation(t *testing.T) {
	t.Parallel()
	before := time.Now().Add(30 * time.Minute)
	ctx, cancel := (qualityGate{}).qualityGateContext(context.Background(), "verify")
	deadline, ok := ctx.Deadline()
	after := time.Now().Add(30 * time.Minute)
	if !ok || deadline.Before(before) || deadline.After(after) {
		cancel()
		t.Fatalf("verify deadline = %v, want within [%v, %v]", deadline, before, after)
	}
	cancel()
	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("directly cancelled context error = %v", ctx.Err())
	}

	parent, cancelParent := context.WithCancel(context.Background())
	child, cancelChild := (qualityGate{}).qualityGateContext(parent, "fast")
	defer cancelChild()
	cancelParent()
	<-child.Done()
	if !errors.Is(child.Err(), context.Canceled) {
		t.Fatalf("parent-cancelled context error = %v", child.Err())
	}
}

func TestCIWorkflowRequiresExactHostedQualityBoundary(t *testing.T) {
	t.Parallel()
	const valid = `name: CI
jobs:
  quality:
    name: Quality (${{ matrix.os }})
    runs-on: ${{ matrix.os }}
    timeout-minutes: 40
    steps: []
`
	for _, test := range []struct {
		name, content string
		wantErr       bool
	}{
		{name: "valid", content: valid},
		{name: "missing", content: "name: CI\njobs: {}\n", wantErr: true},
		{name: "stale timeout", content: strings.Replace(valid, "timeout-minutes: 40", "timeout-minutes: 20", 1), wantErr: true},
		{name: "unbounded", content: strings.Replace(valid, "    timeout-minutes: 40\n", "", 1), wantErr: true},
		{name: "wrong job", content: strings.Replace(valid, "  quality:\n", "  another:\n", 1), wantErr: true},
		{name: "wrong runner expression", content: strings.Replace(valid, "runs-on: ${{ matrix.os }}", "runs-on: ubuntu-latest", 1), wantErr: true},
		{name: "duplicate", content: valid + strings.TrimPrefix(valid, "name: CI\njobs:\n"), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, ".github/workflows/ci.yml", test.content)
			err := (qualityGate{}).checkCIWorkflow(root)
			if test.wantErr && err == nil {
				t.Fatal("checkCIWorkflow() error = nil")
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := (qualityGate{}).checkCIWorkflow(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing workflow error = %v", err)
	}
}

func TestFormattingBatchesPreserveEveryFileWithinWindowsCommandBounds(t *testing.T) {
	t.Parallel()
	files := make([]string, 0, 1000)
	for index := range 1000 {
		files = append(files, filepath.Join(
			strings.Repeat("long-directory-", 8),
			fmt.Sprintf("source-%04d.go", index),
		))
	}
	for _, option := range []string{"-l", "-w"} {
		batches := (sourceFormatter{}).formattingBatches(option, files)
		var flattened []string
		for _, batch := range batches {
			if len(batch) == 0 || len(batch) > maximumFormattingBatchFiles {
				t.Fatalf("formatting batch file count = %d", len(batch))
			}
			bytes := len(option)
			for _, file := range batch {
				bytes += len(file) + 3
			}
			if bytes > maximumFormattingBatchBytes {
				t.Fatalf("formatting batch bytes = %d", bytes)
			}
			flattened = append(flattened, batch...)
		}
		if !slices.Equal(flattened, files) {
			t.Fatal("formatting batches omitted, duplicated, or reordered files")
		}
	}
	if batches := (sourceFormatter{}).formattingBatches("-l", nil); len(batches) != 0 {
		t.Fatalf("empty formatting batches = %#v", batches)
	}
}

func TestNetworkAllowedOnlyForBootstrap(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"fast", "check", "fmt", "verify", "release-artifacts", "unknown"} {
		if (qualityGate{}).networkAllowed(mode) {
			t.Fatalf("networkAllowed(%q) = true", mode)
		}
	}
	if !(qualityGate{}).networkAllowed("tools-bootstrap") {
		t.Fatal("networkAllowed(tools-bootstrap) = false")
	}
}

func TestReleaseArtifactEntrypointRequiresExactExplicitDirectoryContract(t *testing.T) {
	t.Parallel()
	const valid = ".PHONY: verify-release-artifacts\n\nverify-release-artifacts:\n\tgo run ./internal/qualitygate -mode=release-artifacts -artifacts=\"$(SPICE_DISTRIBUTION_VERIFIED_ARTIFACT_DIR)\"\n"
	for _, test := range []struct {
		name, content string
		wantErr       bool
	}{
		{name: "valid", content: valid},
		{name: "missing target", content: strings.Replace(valid, "\nverify-release-artifacts:\n\tgo run ./internal/qualitygate -mode=release-artifacts -artifacts=\"$(SPICE_DISTRIBUTION_VERIFIED_ARTIFACT_DIR)\"\n", "\n", 1), wantErr: true},
		{name: "implicit directory", content: strings.Replace(valid, " -artifacts=\"$(SPICE_DISTRIBUTION_VERIFIED_ARTIFACT_DIR)\"", "", 1), wantErr: true},
		{name: "retired Agent directory", content: strings.Replace(valid, "SPICE_DISTRIBUTION_VERIFIED_ARTIFACT_DIR", "SPICE_AGENT_VERIFIED_ARTIFACT_DIR", 1), wantErr: true},
		{name: "network helper", content: strings.Replace(valid, "go run", "gh release download && go run", 1), wantErr: true},
		{name: "not phony", content: strings.Replace(valid, ".PHONY: verify-release-artifacts", ".PHONY: verify", 1), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, "Makefile", test.content)
			err := (qualityGate{}).checkReleaseArtifactEntrypoint(root)
			if test.wantErr && err == nil {
				t.Fatal("checkReleaseArtifactEntrypoint() error = nil")
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDevelopmentEntrypointsRequireExactVendorEnvironment(t *testing.T) {
	t.Parallel()
	const valid = `dev-daemon dev-terminal: export GOWORK := off
dev-daemon dev-terminal: export GOTOOLCHAIN := local
dev-daemon dev-terminal: export GOFLAGS := -mod=vendor
dev-daemon dev-terminal: export GOPROXY := off
dev-daemon dev-terminal: export GOSUMDB := off
`
	for _, test := range []struct {
		name, content string
		wantErr       bool
	}{
		{name: "valid", content: valid},
		{name: "workspace enabled", content: strings.Replace(valid, "GOWORK := off", "GOWORK := auto", 1), wantErr: true},
		{name: "module cache", content: strings.Replace(valid, "GOFLAGS := -mod=vendor", "GOFLAGS := -mod=mod", 1), wantErr: true},
		{name: "proxy enabled", content: strings.Replace(valid, "GOPROXY := off", "GOPROXY := https://proxy.golang.org", 1), wantErr: true},
		{name: "checksum database enabled", content: strings.Replace(valid, "GOSUMDB := off", "GOSUMDB := sum.golang.org", 1), wantErr: true},
		{name: "duplicate", content: valid + valid, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, "Makefile", test.content)
			err := (qualityGate{}).checkDevelopmentEntrypoints(root)
			if test.wantErr && err == nil {
				t.Fatal("checkDevelopmentEntrypoints() error = nil")
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReleaseArtifactGateUsesOneOfflineBuildTaggedTest(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "verified subjects")
	root := t.TempDir()
	want := []string{
		"test", "-tags=spice_release_artifacts", "-count=1",
		"-run=^TestVerifiedNativeReleaseArchive$", "./internal/installedacceptance",
		"-args", "-spice-release-candidate-root=" + root,
		"-spice-release-artifact-dir=" + directory,
	}
	if got := (moduleVerifier{}).releaseArtifactTestArguments(root, directory); !slices.Equal(got, want) {
		t.Fatalf("release artifact arguments = %q, want %q", got, want)
	}
	if err := (moduleVerifier{}).verifyReleaseArtifacts(t.Context(), t.TempDir(), "relative"); err == nil {
		t.Fatal("release artifact gate accepted a relative directory")
	}
}

func TestReleaseWorkflowRequiresExactKeylessDistributionBoundary(t *testing.T) {
	t.Parallel()
	const (
		stalePreview4WorkflowCommit = "3bf54b986b68d386a90e418776b35ca08f234d20"
	)
	valid := (qualityGate{}).expectedReleaseWorkflow()
	for _, test := range []struct {
		name     string
		workflow string
		omit     bool
	}{
		{name: "valid", workflow: valid},
		{name: "missing", omit: true},
		{
			name:     "stale preview 4 authority",
			workflow: strings.ReplaceAll(valid, releaseWorkflowCommit, stalePreview4WorkflowCommit),
		},
		{name: "wrong workflow", workflow: strings.Replace(valid, "go-distribution-release.yml", "go-module-release.yml", 1)},
		{name: "wrong pin", workflow: strings.Replace(valid, releaseWorkflowCommit, strings.Repeat("0", 40), 1)},
		{name: "wrong input pin", workflow: strings.Replace(valid, "workflow_commit: "+releaseWorkflowCommit, "workflow_commit: "+strings.Repeat("0", 40), 1)},
		{name: "wrong module", workflow: strings.Replace(valid, modulePath, "example.com/wrong", 1)},
		{name: "secret", workflow: valid + "    secrets: inherit\n"},
		{name: "local step", workflow: valid + "    steps:\n      - run: echo unsafe\n"},
		{name: "extra permission", workflow: strings.Replace(valid, "      contents: write\n", "      contents: write\n      actions: write\n", 1)},
		{name: "missing attestation permission", workflow: strings.Replace(valid, "      attestations: write\n", "", 1)},
		{name: "extra job", workflow: valid + "  publish-again:\n    uses: example.invalid/workflow.yml@deadbeef\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if !test.omit {
				writeFile(t, root, ".github/workflows/release.yml", test.workflow)
			}
			err := (qualityGate{}).checkReleaseWorkflow(root)
			if test.name == "valid" && err != nil {
				t.Fatal(err)
			}
			if test.name != "valid" && err == nil {
				t.Fatal("checkReleaseWorkflow() error = nil")
			}
		})
	}
}

func TestReleaseEntrypointRequiresExactVerifyAlias(t *testing.T) {
	t.Parallel()
	valid := ".PHONY: fast verify verify-release\n\nverify:\n\tgo run ./internal/qualitygate -mode=verify\n\nverify-release: verify\n"
	for _, test := range []struct {
		name, content string
		wantErr       bool
	}{
		{name: "valid", content: valid},
		{name: "missing alias", content: strings.Replace(valid, "\nverify-release: verify\n", "\n", 1), wantErr: true},
		{name: "wrong dependency", content: strings.Replace(valid, "verify-release: verify", "verify-release: check", 1), wantErr: true},
		{name: "duplicate alias", content: valid + "verify-release: verify\n", wantErr: true},
		{name: "lookalike alias", content: strings.Replace(valid, "verify-release: verify", "notverify-release: verify", 1), wantErr: true},
		{name: "alias recipe", content: strings.Replace(valid, "verify-release: verify\n", "verify-release: verify\n\tgo run ./internal/qualitygate -mode=fast\n", 1), wantErr: true},
		{name: "not phony", content: strings.Replace(valid, " verify-release", "", 1), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, "Makefile", test.content)
			err := (qualityGate{}).checkReleaseEntrypoint(root)
			if test.wantErr && err == nil {
				t.Fatal("checkReleaseEntrypoint() error = nil")
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := (qualityGate{}).checkReleaseEntrypoint(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing Makefile error = %v", err)
	}
}

func TestExactGoExecutable(t *testing.T) {
	t.Parallel()
	if (commandRunner{}).goExecutableName("windows") != "go.exe" || (commandRunner{}).goExecutableName("linux") != "go" {
		t.Fatal("go executable name is not platform-correct")
	}
	actualName := filepath.Base((commandRunner{}).exactGoExecutable())
	if (actualName != "go" && actualName != "go.exe") || filepath.Base(filepath.Dir((commandRunner{}).exactGoExecutable())) != "bin" || (commandRunner{}).qualityExecutable("go") != (commandRunner{}).exactGoExecutable() || (commandRunner{}).qualityExecutable("gofumpt") != "gofumpt" {
		t.Fatalf("exact Go executable = %q", (commandRunner{}).exactGoExecutable())
	}
}

func TestStyleArgumentsAreStrictAndRepositoryWide(t *testing.T) {
	t.Parallel()

	want := []string{"--config=.spice/style.json", "./..."}
	if got := (sourceFormatter{}).styleArguments(); !slices.Equal(got, want) {
		t.Fatalf("style arguments = %#v, want %#v", got, want)
	}
	wantHostile := map[string]string{
		"CGO_ENABLED":  "1",
		"GOAMD64":      "v4",
		"GOARCH":       "386",
		"GOAUTH":       "netrc",
		"GOENV":        "off",
		"GOEXPERIMENT": "ambientexperiment",
		"GOFIPS140":    "latest",
		"GOFLAGS":      "-tags=ambient",
		"GOOS":         "plan9",
		"GOPROXY":      "http://127.0.0.1:1",
		"GOSUMDB":      "invalid.example",
		"GOTOOLCHAIN":  "go1.99.0+auto",
	}
	if hostile := (sourceFormatter{}).hostileStyleEnvironment(); !maps.Equal(hostile, wantHostile) {
		t.Fatalf("hostile style environment = %#v, want %#v", hostile, wantHostile)
	}
}

func TestGeneratedApplicationChecksCoverAllTargetsAndModes(t *testing.T) {
	t.Parallel()
	checks := (moduleVerifier{}).generatedApplicationChecks()
	if len(checks) != 6 {
		t.Fatalf("generated checks = %d, want 6", len(checks))
	}
	want := []string{
		"ArchitectureProof --check ./internal/architectureproof",
		"ArchitectureProof --diff ./internal/architectureproof",
		"spice-agentd --check ./cmd/spice-agentd",
		"spice-agentd --diff ./cmd/spice-agentd",
		"spice-agent --check ./cmd/spice-agent",
		"spice-agent --diff ./cmd/spice-agent",
	}
	for index, arguments := range checks {
		if len(arguments) != 8 || arguments[0] != "tool" || arguments[2] != "generate" ||
			arguments[4] != "--target" || arguments[6] != "." {
			t.Fatalf("generated check %d malformed: %q", index, arguments)
		}
		got := arguments[5] + " " + arguments[3] + " " + arguments[7]
		if got != want[index] {
			t.Fatalf("generated check %d = %q, want %q", index, got, want[index])
		}
	}
}

func TestBootstrapDownloadArguments(t *testing.T) {
	t.Parallel()
	moduleFile := filepath.Join("private", "graph.mod")
	want := "mod download -modfile=" + moduleFile + " all"
	if got := strings.Join((dependencyBootstrapper{}).bootstrapDownloadArguments(moduleFile), " "); got != want {
		t.Fatalf("bootstrapDownloadArguments() = %q, want %q", got, want)
	}
}

func TestBootstrapPreservesRepositoryOnSuccessFailureAndCancellation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		runnerErr error
	}{
		{name: "success"},
		{name: "failure", runnerErr: errors.New("download failed")},
		{name: "cancellation", runnerErr: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := bootstrapFixture(t, true)
			before, err := (moduleVerifier{}).sourceTreeDigests(root)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if errors.Is(test.runnerErr, context.Canceled) {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			var calls [][]string
			runner := func(callContext context.Context, directory string, arguments ...string) error {
				if directory != root && directory != filepath.Join(root, "tools") {
					t.Fatalf("unexpected directory %q", directory)
				}
				calls = append(calls, append([]string(nil), arguments...))
				if errors.Is(test.runnerErr, context.Canceled) {
					return callContext.Err()
				}
				return test.runnerErr
			}
			err = (dependencyBootstrapper{}).bootstrapDependencies(ctx, root, runner)
			if !errors.Is(err, test.runnerErr) {
				t.Fatalf("bootstrapDependencies() error = %v, want %v", err, test.runnerErr)
			}
			after, digestErr := (moduleVerifier{}).sourceTreeDigests(root)
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			if len(before) != len(after) {
				t.Fatalf("repository file count changed: %d != %d", len(before), len(after))
			}
			for name, digest := range before {
				if after[name] != digest {
					t.Fatalf("repository file %q changed", name)
				}
			}
			wantCalls := 2
			if test.runnerErr != nil {
				wantCalls = 1
			}
			if len(calls) != wantCalls {
				t.Fatalf("bootstrap calls = %d, want %d", len(calls), wantCalls)
			}
			for _, arguments := range calls {
				if len(arguments) != 4 || arguments[0] != "mod" || arguments[1] != "download" ||
					!strings.HasPrefix(arguments[2], "-modfile=") || arguments[3] != "all" {
					t.Fatalf("unexpected bootstrap arguments: %q", arguments)
				}
				if strings.HasPrefix(strings.TrimPrefix(arguments[2], "-modfile="), root) {
					t.Fatalf("temporary modfile is inside repository: %q", arguments[2])
				}
			}
		})
	}
}

func TestBootstrapDetectsRepositoryMutation(t *testing.T) {
	t.Parallel()
	root := bootstrapFixture(t, false)
	err := (dependencyBootstrapper{}).bootstrapDependencies(context.Background(), root, func(_ context.Context, directory string, _ ...string) error {
		return os.WriteFile(filepath.Join(directory, "unexpected"), []byte("mutation"), 0o600)
	})
	if err == nil || !strings.Contains(err.Error(), "modified the repository") {
		t.Fatalf("bootstrapDependencies() error = %v", err)
	}
}

func TestBootstrapAllowsMissingToolsModule(t *testing.T) {
	t.Parallel()
	root := bootstrapFixture(t, false)
	calls := 0
	err := (dependencyBootstrapper{}).bootstrapDependencies(context.Background(), root, func(_ context.Context, _ string, _ ...string) error {
		calls++
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("bootstrapDependencies() = calls %d, error %v", calls, err)
	}
}

func TestBootstrapEnvironmentRejectsCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("SPICE_TEST_TOKEN", "must-not-leak")
	environment := strings.Join((commandRunner{}).commandEnvironment(true, nil), "\n")
	for _, required := range []string{
		"GOAUTH=off", "GOPROXY=https://proxy.golang.org", "GOSUMDB=sum.golang.org",
	} {
		if !strings.Contains(environment, required) {
			t.Fatalf("bootstrap environment lacks %q:\n%s", required, environment)
		}
	}
	if strings.Contains(environment, "must-not-leak") {
		t.Fatalf("bootstrap environment contains an application credential:\n%s", environment)
	}
}

func TestReleaseArtifactEnvironmentPropagatesGenericDistributionContract(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "verified subjects")
	t.Setenv("SPICE_DISTRIBUTION_VERIFIED_ARTIFACT_DIR", directory)
	t.Setenv("SPICE_DISTRIBUTION_EPHEMERAL_RUNNER", "1")
	environment := (commandRunner{}).commandEnvironment(false, nil)
	for _, required := range []string{
		"SPICE_DISTRIBUTION_VERIFIED_ARTIFACT_DIR=" + directory,
		"SPICE_DISTRIBUTION_EPHEMERAL_RUNNER=1",
	} {
		if !slices.Contains(environment, required) {
			t.Fatalf("release-artifact environment lacks %q:\n%s", required, strings.Join(environment, "\n"))
		}
	}
}

func bootstrapFixture(t *testing.T, tools bool) string {
	t.Helper()
	root := t.TempDir()
	modules := []string{root}
	if tools {
		modules = append(modules, filepath.Join(root, "tools"))
	}
	for _, directory := range modules {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.com/fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "go.sum"), []byte("fixture sum\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestValidateCompatibility(t *testing.T) {
	t.Parallel()
	valid := compatibilityFixture()
	tests := []struct {
		name, content, wantErr string
	}{
		{name: "valid", content: valid},
		{name: "malformed", content: `{`, wantErr: "decode"},
		{name: "unknown", content: strings.Replace(valid, `}`, `,"extra":true}`, 1), wantErr: "unknown field"},
		{name: "trailing", content: valid + `{}`, wantErr: "trailing"},
		{name: "wrong Go", content: strings.Replace(valid, "1.26.5", "1.26.4", 1), wantErr: "Go 1.26.5"},
		{name: "wrong Spice", content: replaceCompatibilitySelection(valid, "spice", spiceVersion), wantErr: "spice"},
		{name: "wrong toolchain", content: replaceCompatibilitySelection(valid, "spice_toolchain", toolchainVersion), wantErr: "spice_toolchain"},
		{name: "wrong core", content: replaceCompatibilitySelection(valid, "spice_agent", agentVersion), wantErr: "spice_agent"},
		{name: "wrong TUI", content: replaceCompatibilitySelection(valid, "spice_agent_tui", agentTUIVersion), wantErr: "spice_agent_tui"},
		{name: "wrong provider", content: replaceCompatibilitySelection(valid, "spice_agent_provider_openai", providerVersion), wantErr: "spice_agent_provider_openai"},
		{name: "wrong coding tools", content: replaceCompatibilitySelection(valid, "spice_agent_tools_coding", codingToolsVersion), wantErr: "spice_agent_tools_coding"},
		{name: "missing selection", content: strings.Replace(valid, `,"spice_agent_tui":"`+agentTUIVersion+`"`, "", 1), wantErr: "spice_agent_tui"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := (qualityGate{}).validateCompatibility([]byte(test.content))
			if test.wantErr == "" && err != nil {
				t.Fatalf("validateCompatibility() error = %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("validateCompatibility() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func replaceCompatibilitySelection(content, name, oldVersion string) string {
	oldSelection := `"` + name + `":"` + oldVersion + `"`
	newSelection := `"` + name + `":"v1.0.0"`
	return strings.Replace(content, oldSelection, newSelection, 1)
}

func TestIdentityAndPins(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "go.mod", strings.Join([]string{
		"module " + modulePath,
		"go 1.26.0",
		"toolchain go1.26.5",
		"tool github.com/spice-framework/spice-agent-tui/cmd/spice-agent-tui-annotations",
		"tool github.com/spice-framework/spice-agent/cmd/spice-agent-annotations",
		"tool github.com/spice-framework/toolchain/cmd/spice",
		"tool github.com/spice-framework/toolchain/cmd/spice-annotation-core",
		"require github.com/spice-framework/spice " + spiceVersion,
		"require github.com/spice-framework/toolchain " + toolchainVersion,
		"require github.com/spice-framework/spice-agent " + agentVersion,
		"require github.com/spice-framework/spice-agent-tui " + agentTUIVersion,
		"require github.com/spice-framework/spice-agent-provider-openai " + providerVersion,
		"require github.com/spice-framework/spice-agent-tools-coding " + codingToolsVersion,
	}, "\n")+"\n")
	writeFile(t, root, "compatibility.json", compatibilityFixture())
	writeFile(t, root, "tools/go.mod", strings.Join([]string{
		"github.com/golangci/golangci-lint/v2 v2.12.2",
		"github.com/securego/gosec/v2 v2.28.0",
		"github.com/spice-framework/toolchain " + toolchainVersion,
		"go.uber.org/nilaway v0.0.0-20260724203407-f4f8ac24c032",
		"golang.org/x/tools v0.48.0", "golang.org/x/vuln v1.1.4", "mvdan.cc/gofumpt v0.10.0",
	}, "\n"))
	if err := (qualityGate{}).checkIdentity(root); err != nil {
		t.Fatalf("checkIdentity() error = %v", err)
	}
	writeFile(t, root, "go.mod", "module example.com/wrong\n")
	if err := (qualityGate{}).checkIdentity(root); err == nil || !strings.Contains(err.Error(), "canonical selection") {
		t.Fatalf("checkIdentity() error = %v, want identity diagnostic", err)
	}
}

func compatibilityFixture() string {
	return `{"schema":1,"go":"1.26.5","spice":"` + spiceVersion +
		`","spice_toolchain":"` + toolchainVersion +
		`","spice_agent":"` + agentVersion +
		`","spice_agent_tui":"` + agentTUIVersion + `","spice_agent_provider_openai":"` + providerVersion +
		`","spice_agent_tools_coding":"` + codingToolsVersion + `"}`
}

func TestFilesCoverageAndModeBoundaries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "root.go", "package fixture")
	writeFile(t, root, "internal/value.go", "package value")
	writeFile(t, root, "tools/ignored.go", "package ignored")
	files, err := (sourceFormatter{}).goFiles(root)
	if err != nil || len(files) != 2 || !slices.IsSorted(files) {
		t.Fatalf("goFiles() = %v, %v", files, err)
	}
	digests, err := (moduleVerifier{}).treeDigests(root)
	if err != nil || len(digests) != 3 {
		t.Fatalf("treeDigests() = %d, %v", len(digests), err)
	}
	missing, err := (moduleVerifier{}).treeDigests(filepath.Join(root, "missing"))
	if err != nil || len(missing) != 0 {
		t.Fatalf("treeDigests(missing) = %v, %v", missing, err)
	}
	percentage, err := (testVerifier{}).totalCoverage("total: (statements) 90.0%")
	if err != nil || percentage != 90 {
		t.Fatalf("totalCoverage() = %v, %v", percentage, err)
	}
	if _, err := (testVerifier{}).totalCoverage("invalid"); err == nil {
		t.Fatal("totalCoverage(invalid) error = nil")
	}
	if err := (qualityGate{}).run(t.Context(), root, "unknown"); err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("run(unknown) error = %v", err)
	}
}

func TestExcludeGeneratedCoverage(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "coverage.out")
	content := "mode: atomic\n" +
		modulePath + "/internal/spicegen/proof/generated.go:1.1,2.1 1 1\n" +
		modulePath + "/internal/architectureproof/proof.go:1.1,2.1 1 1\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (testVerifier{}).excludeGeneratedCoverage(path); err != nil {
		t.Fatal(err)
	}
	filtered, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes := string(filtered); strings.Contains(bytes, "/internal/spicegen/") ||
		!strings.Contains(bytes, "/internal/architectureproof/") {
		t.Fatalf("filtered coverage = %q", bytes)
	}
}

func TestCoverageTestPackagesExcludeOnlyProcessAcceptance(t *testing.T) {
	t.Parallel()
	packages := []string{
		modulePath + "/internal/daemon",
		modulePath + "/internal/devacceptance",
		modulePath + "/internal/installedacceptance",
		modulePath + "/internal/processcontainment",
	}
	want := []string{
		modulePath + "/internal/daemon",
		modulePath + "/internal/processcontainment",
	}
	if got := (testVerifier{}).coverageTestPackages(packages); !slices.Equal(got, want) {
		t.Fatalf("coverage test packages = %v, want %v", got, want)
	}
	if len(packages) != 4 {
		t.Fatalf("coverage package selection mutated caller input: %v", packages)
	}
}

func TestEnvironmentAndCancellation(t *testing.T) {
	t.Parallel()
	offline := (commandRunner{}).commandEnvironment(false, map[string]string{"GOFLAGS": "-mod=vendor"})
	if !slices.Contains(offline, "GOPROXY=off") || !slices.Contains(offline, "GOWORK=off") ||
		!slices.Contains(offline, "GOFLAGS=-mod=vendor") {
		t.Fatalf("offline environment is not isolated")
	}
	for _, entry := range offline {
		upper := strings.ToUpper(entry)
		if strings.Contains(upper, "TOKEN=") || strings.Contains(upper, "SECRET=") {
			t.Fatalf("credential-like environment was inherited: %s", entry)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := (commandRunner{}).command(ctx, t.TempDir(), nil, "go", "version")
	if err == nil || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("command(cancelled) error = %v", err)
	}
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
