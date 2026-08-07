package daemon_test

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

	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/daemon"
	spiceconfig "github.com/spice-framework/spice/config"
)

const (
	spiceTool        = "github.com/spice-framework/toolchain/cmd/spice"
	daemonTestSecret = "daemon-generated-test-secret"
	daemonTestModel  = "daemon-test-model"
)

func TestDaemonGenerationAndBeanExplanationAreCurrent(t *testing.T) {
	root := repositoryRoot(t)
	for _, arguments := range [][]string{
		{"generate", "--check", "--target", "Daemon", ".", "./internal/daemon"},
		{"generate", "--diff", "--target", "Daemon", ".", "./internal/daemon"},
	} {
		stdout, stderr, err := runSpice(t, root, arguments...)
		if err != nil || stderr != "" || !strings.Contains(stdout, "generation is current") {
			t.Fatalf("spice %v = stdout %q, stderr %q, error %v", arguments, stdout, stderr, err)
		}
	}
	stdout, stderr, err := runSpice(
		t, root, "beans", "--explain", "--format=json", "./internal/daemon",
	)
	if err != nil || stderr != "" {
		t.Fatalf("spice beans = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	for _, expected := range []string{
		`"name": "daemonRootRegistry"`,
		`"name": "openAIModelProvider"`,
		`"name": "read"`,
		`"name": "replace"`,
		`"name": "shell"`,
		`"name": "daemonRuntime"`,
	} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("bean explanation lacks %q: %s", expected, stdout)
		}
	}
}

func TestGeneratedDaemonConstructsInspectableGraphWithoutPublication(t *testing.T) {
	t.Parallel()
	values, err := spiceconfig.NewMapSource("test", map[string]string{
		"agent.openai.api-key": daemonTestSecret,
		"agent.model":          daemonTestModel,
		"agent.workspace":      t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := spicegen.NewApplicationWithOptions(
		context.Background(),
		spicegen.ApplicationOptions{Sources: []spiceconfig.Source{values}},
	)
	if err != nil {
		t.Fatalf("construct generated daemon: %v", err)
	}
	components := application.Components()
	if components.DaemonRootRegistry == nil || components.DaemonRoot == nil ||
		components.DaemonEngine == nil || components.RunHost == nil ||
		components.GrpcServer == nil || components.DaemonRuntime == nil ||
		components.Read == nil || components.Replace == nil || components.Shell == nil {
		t.Fatal("generated daemon graph is incomplete")
	}
	if components.Properties.Model != daemonTestModel || components.OpenAIConfig.APIKey != daemonTestSecret {
		t.Fatal("generated typed configuration was not injected")
	}
	stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = application.Stop(stopContext); err != nil {
		t.Fatalf("stop generated daemon: %v", err)
	}
}

func TestGeneratedDaemonIsDirectAndContainmentAdoptionPrecedesChildCapableBeans(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	providers, err := os.ReadFile(filepath.Join(root, "internal", "spicegen", "daemon", "spice_providers_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	registry := bytes.Index(providers, []byte("ConstructDaemonRootRegistry"))
	codingConfig := bytes.Index(providers, []byte("ConstructCodingConfig"))
	readTool := bytes.Index(providers, []byte("ConstructRead"))
	if registry < 0 || codingConfig <= registry || readTool <= codingConfig {
		t.Fatalf("generated containment order registry=%d coding=%d read=%d", registry, codingConfig, readTool)
	}
	for _, expected := range []string{
		"ConstructDaemonRuntime", "ConstructGrpcServer", "ConstructRunHost",
		`map[string]tool.Tool{"read": read, "replace": replace, "shell": shell}`,
	} {
		if !bytes.Contains(providers, []byte(expected)) {
			t.Fatalf("generated provider file lacks %q", expected)
		}
	}
	for _, forbidden := range []string{"reflect.", "RuntimeGraph", "ServiceLocator", daemonTestSecret} {
		if bytes.Contains(providers, []byte(forbidden)) {
			t.Fatalf("generated provider file contains forbidden %q", forbidden)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(root, ".spice", "daemon.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"layout": "generated-package"`, `"role": "source-unit"`,
		"internal/daemon/root.go", "NewRootRegistry", "NewRuntime",
		"github.com/spice-framework/spice-agent-tools-coding/autoconfigure/autoconfigure.go",
	} {
		if !bytes.Contains(manifest, []byte(expected)) {
			t.Fatalf("generation manifest lacks %q", expected)
		}
	}
	if bytes.Contains(manifest, []byte(daemonTestSecret)) {
		t.Fatal("generation manifest contains a configuration secret")
	}
}

func runSpice(t *testing.T, root string, arguments ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	commandArguments := append([]string{"tool", spiceTool}, arguments...)
	// #nosec G204,G702 -- executable and arguments are fixed daemon generation checks.
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
	return filepath.Join(runtime.GOROOT(), "bin", name) //nolint:staticcheck // Use the exact executing Go toolchain.
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
