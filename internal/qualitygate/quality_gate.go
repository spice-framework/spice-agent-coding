package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// qualityGate owns repository-contract orchestration and mode selection.
type qualityGate struct{}

func (owner qualityGate) execute() int {
	mode := flag.String("mode", "verify", "verification mode: tools-bootstrap, fast, check, coverage, fmt, verify, release-artifacts, or opencode-eval")
	artifacts := flag.String("artifacts", "", "absolute independently verified release-subject directory")
	flag.Parse()
	ctx, cancel := (qualityGate{}).qualityGateContext(context.Background(), *mode)
	defer cancel()
	root, err := (commandRunner{}).repositoryRoot()
	if err == nil {
		err = (qualityGate{}).runConfigured(ctx, root, *mode, *artifacts)
	}
	if err == nil {
		return 0
	}
	if _, writeErr := fmt.Fprintf(os.Stdout, "quality gate failed: %v\n", err); writeErr != nil {
		return 1
	}
	return 1
}

func (owner qualityGate) qualityGateTimeout(mode string) time.Duration {
	switch mode {
	case "verify":
		return 30 * time.Minute
	case "opencode-eval":
		return maximumOpenCodeEvaluationDuration + time.Minute
	default:
		return 15 * time.Minute
	}
}

func (owner qualityGate) qualityGateContext(parent context.Context, mode string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, owner.qualityGateTimeout(mode))
}

func (owner qualityGate) run(ctx context.Context, root, mode string) error {
	return (qualityGate{}).runConfigured(ctx, root, mode, "")
}

func (owner qualityGate) runConfigured(ctx context.Context, root, mode, artifacts string) error {
	if runtime.Version() != requiredGoVersion {
		return fmt.Errorf("go version is %s; require exactly %s", runtime.Version(), requiredGoVersion)
	}
	identity := step{"repository identity", func() error { return (qualityGate{}).checkRepositoryContract(root) }}
	bootstrap := step{"explicit dependency bootstrap", func() error {
		return (dependencyBootstrapper{}).bootstrapDependencies(ctx, root, (commandRunner{}).networkCommand)
	}}
	formatting := step{"formatting", func() error { return (sourceFormatter{}).format(ctx, root, false) }}
	modules := step{"module and vendor", func() error { return (moduleVerifier{}).checkModule(ctx, root) }}
	generated := step{"generated applications", func() error { return (moduleVerifier{}).checkGeneratedApplications(ctx, root) }}
	style := step{"Spice application style", func() error { return (sourceFormatter{}).checkStyle(ctx, root) }}
	vet := step{"go vet", func() error { return (commandRunner{}).command(ctx, root, nil, "go", "vet", "./...") }}
	fastTest := step{"affected-loop tests", func() error { return (testVerifier{}).tests(ctx, root, false, false) }}
	test := step{"shuffled tests", func() error { return (testVerifier{}).tests(ctx, root, false, true) }}
	var steps []step
	if (qualityGate{}).networkAllowed(mode) {
		steps = []step{identity, bootstrap}
	} else {
		switch mode {
		case "fast":
			steps = []step{identity, fastTest}
		case "check":
			steps = []step{identity, formatting, modules, generated, style, vet, test}
		case "coverage":
			steps = []step{identity, {"coverage", func() error { return (testVerifier{}).coverage(ctx, root) }}}
		case "fmt":
			steps = []step{identity, {"formatting write", func() error { return (sourceFormatter{}).format(ctx, root, true) }}}
		case "verify":
			steps = []step{
				identity, formatting, modules, generated, style, vet,
				{"lint and nil safety", func() error { return (testVerifier{}).lint(ctx, root) }},
				{"security", func() error { return (testVerifier{}).security(ctx, root) }},
				test,
				{"race tests", func() error { return (testVerifier{}).tests(ctx, root, true, true) }},
				{"coverage", func() error { return (testVerifier{}).coverage(ctx, root) }},
				{"offline vendor", func() error { return (testVerifier{}).offline(ctx, root) }},
			}
		case "release-artifacts":
			steps = []step{
				identity,
				{"verified release archive", func() error {
					return (moduleVerifier{}).verifyReleaseArtifacts(ctx, root, artifacts)
				}},
			}
		case "opencode-eval":
			steps = []step{
				{"advisory OpenCode free-model evaluation", func() error {
					return (opencodeEvaluation{}).newOpenCodeEvaluation().Run(ctx, root, os.Stdout)
				}},
			}
		default:
			return fmt.Errorf("unknown mode %q", mode)
		}
	}
	for _, current := range steps {
		started := time.Now()
		if _, err := fmt.Fprintf(os.Stdout, "==> %s\n", current.name); err != nil {
			return err
		}
		if err := current.run(); err != nil {
			return fmt.Errorf("%s (%s): %w", current.name, time.Since(started).Round(time.Millisecond), err)
		}
		if _, err := fmt.Fprintf(os.Stdout, "<== %s passed in %s\n", current.name, time.Since(started).Round(time.Millisecond)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(os.Stdout, "==> all verification passed")
	return err
}

func (owner qualityGate) networkAllowed(mode string) bool { return mode == "tools-bootstrap" }

func (owner qualityGate) checkRepositoryContract(root string) error {
	if err := (qualityGate{}).checkIdentity(root); err != nil {
		return err
	}
	if err := (releaseMetadata{}).checkReleaseMetadata(root); err != nil {
		return err
	}
	if err := (qualityGate{}).checkReleaseEntrypoint(root); err != nil {
		return err
	}
	if err := (qualityGate{}).checkReleaseArtifactEntrypoint(root); err != nil {
		return err
	}
	if err := (qualityGate{}).checkDevelopmentEntrypoints(root); err != nil {
		return err
	}
	if err := (qualityGate{}).checkCIWorkflow(root); err != nil {
		return err
	}
	return (qualityGate{}).checkReleaseWorkflow(root)
}

func (owner qualityGate) checkCIWorkflow(root string) error {
	content, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml")) // #nosec G304 -- fixed repository workflow path.
	if err != nil {
		return fmt.Errorf("read CI workflow: %w", err)
	}
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	const qualityBoundary = `  quality:
    name: Quality (${{ matrix.os }})
    runs-on: ${{ matrix.os }}
    timeout-minutes: 40
`
	if strings.Count(normalized, qualityBoundary) != 1 {
		return errors.New("CI workflow Quality job must have the exact 40-minute hosted boundary")
	}
	return nil
}

func (owner qualityGate) checkDevelopmentEntrypoints(root string) error {
	content, err := os.ReadFile(filepath.Join(root, "Makefile")) // #nosec G304 -- fixed repository build contract.
	if err != nil {
		return fmt.Errorf("read Makefile: %w", err)
	}
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	const environment = `dev-daemon dev-terminal: export GOWORK := off
dev-daemon dev-terminal: export GOTOOLCHAIN := local
dev-daemon dev-terminal: export GOFLAGS := -mod=vendor
dev-daemon dev-terminal: export GOPROXY := off
dev-daemon dev-terminal: export GOSUMDB := off
`
	if strings.Count(normalized, environment) != 1 {
		return errors.New("development targets must use the exact workspace-disabled vendor-only Go environment")
	}
	return nil
}

func (owner qualityGate) checkReleaseArtifactEntrypoint(root string) error {
	content, err := os.ReadFile(filepath.Join(root, "Makefile")) // #nosec G304 -- fixed repository build contract.
	if err != nil {
		return fmt.Errorf("read Makefile: %w", err)
	}
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	const target = "verify-release-artifacts:\n\tgo run ./internal/qualitygate -mode=release-artifacts -artifacts=\"$(SPICE_DISTRIBUTION_VERIFIED_ARTIFACT_DIR)\"\n"
	if strings.Count(normalized, target) != 1 {
		return errors.New("makefile must expose the exact independently verified release-artifact gate")
	}
	for line := range strings.SplitSeq(normalized, "\n") {
		if targets, ok := strings.CutPrefix(line, ".PHONY:"); ok {
			if !slices.Contains(strings.Fields(targets), "verify-release-artifacts") {
				return errors.New("makefile must declare verify-release-artifacts phony")
			}
			return nil
		}
	}
	return errors.New("makefile has no .PHONY declaration")
}

func (owner qualityGate) checkReleaseEntrypoint(root string) error {
	content, err := os.ReadFile(filepath.Join(root, "Makefile")) // #nosec G304 -- fixed repository build contract.
	if err != nil {
		return fmt.Errorf("read Makefile: %w", err)
	}
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	definitions := 0
	exact := -1
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "verify-release:") {
			definitions++
			if line == "verify-release: verify" {
				exact = index
			}
		}
	}
	if definitions != 1 || exact < 0 || exact+1 >= len(lines) || lines[exact+1] != "" {
		return errors.New("makefile must expose verify-release exactly once as an alias of verify")
	}
	for _, line := range lines {
		if targets, ok := strings.CutPrefix(line, ".PHONY:"); ok {
			if !slices.Contains(strings.Fields(targets), "verify-release") {
				return errors.New("makefile must declare verify-release phony")
			}
			return nil
		}
	}
	return errors.New("makefile has no .PHONY declaration")
}

func (owner qualityGate) checkReleaseWorkflow(root string) error {
	content, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml")) // #nosec G304 -- fixed repository workflow path.
	if err != nil {
		return fmt.Errorf("read release workflow: %w", err)
	}
	if strings.ReplaceAll(string(content), "\r\n", "\n") != (qualityGate{}).expectedReleaseWorkflow() {
		return errors.New("release workflow must be the exact keyless distribution caller with no secrets or local steps")
	}
	return nil
}

func (owner qualityGate) expectedReleaseWorkflow() string {
	return `name: Release

on:
  push:
    tags:
      - "v[0-9]*.[0-9]*.[0-9]*"

permissions: {}

jobs:
  release:
    name: Keylessly attest and publish distribution
    permissions:
      contents: write
      id-token: write
      attestations: write
      artifact-metadata: write
    uses: spice-framework/.github/.github/workflows/go-distribution-release.yml@` + releaseWorkflowCommit + `
    with:
      module: ` + modulePath + `
      workflow_commit: ` + releaseWorkflowCommit + `
`
}

func (owner qualityGate) checkIdentity(root string) error {
	content, err := os.ReadFile(filepath.Join(root, "go.mod")) // #nosec G304 -- root is repository-owned.
	if err != nil {
		return fmt.Errorf("read go.mod: %w", err)
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	for _, required := range []string{
		"module " + modulePath + "\n",
		"\ngo 1.26.0\n",
		"\ntoolchain go1.26.5\n",
		"github.com/spice-framework/spice " + spiceVersion,
		"github.com/spice-framework/toolchain " + toolchainVersion,
		"github.com/spice-framework/spice-agent " + agentVersion,
		"github.com/spice-framework/spice-agent-tui " + agentTUIVersion,
		"github.com/spice-framework/spice-agent-provider-openai " + providerVersion,
		"github.com/spice-framework/spice-agent-tools-coding " + codingToolsVersion,
		"github.com/spice-framework/spice-agent/cmd/spice-agent-annotations",
		"github.com/spice-framework/spice-agent-tui/cmd/spice-agent-tui-annotations",
		"github.com/spice-framework/toolchain/cmd/spice",
		"github.com/spice-framework/toolchain/cmd/spice-annotation-core",
	} {
		if !strings.Contains(text, required) {
			return fmt.Errorf("go.mod is missing canonical selection %q", required)
		}
	}
	if strings.Contains(text, "\nreplace ") || strings.Contains(text, "\nreplace (") {
		return errors.New("committed go.mod must not contain replace directives")
	}
	compatibilityContent, err := os.ReadFile(filepath.Join(root, "compatibility.json")) // #nosec G304 -- fixed repository file.
	if err != nil {
		return fmt.Errorf("read compatibility metadata: %w", err)
	}
	if err := (qualityGate{}).validateCompatibility(compatibilityContent); err != nil {
		return err
	}
	return (qualityGate{}).validateToolPins(filepath.Join(root, "tools", "go.mod"))
}

func (owner qualityGate) validateCompatibility(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var value compatibility
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode compatibility metadata: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("compatibility metadata has trailing JSON values")
	}
	if value.Schema != 1 || value.Go != "1.26.5" {
		return errors.New("compatibility metadata must record schema 1 and Go 1.26.5")
	}
	selections := []struct {
		name string
		got  *string
		want string
	}{
		{name: "spice", got: value.Spice, want: spiceVersion},
		{name: "spice_toolchain", got: value.SpiceToolchain, want: toolchainVersion},
		{name: "spice_agent", got: value.SpiceAgent, want: agentVersion},
		{name: "spice_agent_tui", got: value.SpiceAgentTUI, want: agentTUIVersion},
		{name: "spice_agent_provider_openai", got: value.SpiceAgentProviderOpenAI, want: providerVersion},
		{name: "spice_agent_tools_coding", got: value.SpiceAgentToolsCoding, want: codingToolsVersion},
	}
	for _, selection := range selections {
		if selection.got == nil || *selection.got != selection.want {
			return fmt.Errorf("compatibility metadata %s must select %s", selection.name, selection.want)
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil {
		return fmt.Errorf("decode compatibility metadata fields: %w", err)
	}
	for _, name := range []string{
		"spice", "spice_toolchain", "spice_agent", "spice_agent_tui",
		"spice_agent_provider_openai", "spice_agent_tools_coding",
	} {
		if _, present := fields[name]; !present {
			return fmt.Errorf("compatibility metadata field %q must be present", name)
		}
	}
	return nil
}

func (owner qualityGate) validateToolPins(path string) error {
	content, err := os.ReadFile(path) // #nosec G304 -- fixed tools module path.
	if err != nil {
		return fmt.Errorf("read tools module: %w", err)
	}
	for _, pin := range []string{
		"github.com/golangci/golangci-lint/v2 v2.12.2",
		"github.com/securego/gosec/v2 v2.28.0",
		"github.com/spice-framework/toolchain " + toolchainVersion,
		"go.uber.org/nilaway v0.0.0-20260724203407-f4f8ac24c032",
		"golang.org/x/tools v0.48.0", "golang.org/x/vuln v1.1.4", "mvdan.cc/gofumpt v0.10.0",
	} {
		if !bytes.Contains(content, []byte(pin)) {
			return fmt.Errorf("tools module is missing exact pin %s", pin)
		}
	}
	return nil
}
