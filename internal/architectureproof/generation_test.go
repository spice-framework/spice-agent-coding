package architectureproof_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const spiceTool = "github.com/spice-framework/toolchain/cmd/spice"

func TestArchitectureProofGenerationAndBeanExplanationAreCurrent(t *testing.T) {
	root := repositoryRoot(t)
	for _, arguments := range [][]string{
		{"generate", "--check", "--target", "ArchitectureProof", ".", "./internal/architectureproof"},
		{"generate", "--diff", "--target", "ArchitectureProof", ".", "./internal/architectureproof"},
	} {
		stdout, stderr, err := runSpice(t, root, arguments...)
		if err != nil || stderr != "" || !strings.Contains(stdout, "generation is current") {
			t.Fatalf("spice %v = stdout %q, stderr %q, error %v", arguments, stdout, stderr, err)
		}
	}
	stdout, stderr, err := runSpice(
		t,
		root,
		"beans", "--explain", "--format=json", "./internal/architectureproof",
	)
	if err != nil || stderr != "" {
		t.Fatalf("spice beans = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	for _, expected := range []string{
		`"name": "architecture-proof-openai"`,
		`"status": "replaced"`,
		"default bean openAIModelProvider",
		`"fallback": true`,
		`"name": "read"`,
		`"name": "replace"`,
		`"name": "shell"`,
	} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("bean explanation lacks %q: %s", expected, stdout)
		}
	}
}

func TestGeneratedArchitectureProofContainsInspectableDirectCalls(t *testing.T) {
	root := repositoryRoot(t)
	providers, err := os.ReadFile(filepath.Join(
		root,
		"internal", "spicegen", "architectureproof", "spice_providers_gen.go",
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"ConstructCodingConfig", "ConstructArchitectureProofOpenai", "ConstructArchitectureProofToolDispatcher",
		"ConstructArchitectureProofToolPlanSource", "ConstructArchitectureProofInteractionBroker",
		"ConstructArchitectureProofExecutionPlan", "ConstructArchitectureProofEngine", "ConstructProof",
		`map[string]tool.Tool{"read": read, "replace": replace, "shell": shell}`,
	} {
		if !bytes.Contains(providers, []byte(expected)) {
			t.Fatalf("generated provider file lacks %q", expected)
		}
	}
	for _, forbidden := range []string{
		"reflect.", "RuntimeGraph", "ServiceLocator", "ExtensionRegistry",
	} {
		if bytes.Contains(providers, []byte(forbidden)) {
			t.Fatalf("generated provider file contains forbidden %q", forbidden)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(root, ".spice", "architectureproof.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"layout": "generated-package"`,
		`"role": "source-unit"`,
		`"kind": "provider-construction"`,
		"github.com/spice-framework/spice-agent-tools-coding/autoconfigure/autoconfigure.go",
		"internal/architectureproof/proof.go",
		"internal/architectureproof/plan.go",
		"NewExecutionPlanMetadata",
		"NewToolPlanSource",
		"NewEngine",
	} {
		if !bytes.Contains(manifest, []byte(expected)) {
			t.Fatalf("generation manifest lacks %q", expected)
		}
	}
	if bytes.Contains(providers, []byte(fixtureSecretForScan)) || bytes.Contains(manifest, []byte(fixtureSecretForScan)) {
		t.Fatal("generated output or ownership manifest contains the provider fixture credential")
	}
}

const fixtureSecretForScan = "architecture-proof-secret"

func runSpice(t *testing.T, root string, arguments ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	commandArguments := append([]string{"tool", spiceTool}, arguments...)
	// #nosec G204,G702 -- executable and arguments are fixed architecture-proof commands.
	command := exec.CommandContext(ctx, exactGoExecutable(), commandArguments...)
	command.Dir = root
	command.Env = append(
		os.Environ(),
		"GOWORK=off", "GOPROXY=off", "GOFLAGS=-mod=vendor", "GOTOOLCHAIN=local",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() != nil {
		t.Fatalf("spice %v timed out: %v", arguments, ctx.Err())
	}
	return stdout.String(), stderr.String(), err
}

func exactGoExecutable() string {
	name := "go"
	if runtime.GOOS == "windows" {
		name = "go.exe"
	}
	return filepath.Join(runtime.GOROOT(), "bin", name) //nolint:staticcheck // Test must use the exact Go toolchain executing it.
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	current, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		content, readErr := os.ReadFile(filepath.Join(current, "go.mod"))
		if readErr == nil && bytes.Contains(content, []byte("module github.com/spice-framework/spice-agent-coding\n")) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatal("locate spice-agent-coding module root")
		}
		current = parent
	}
}
