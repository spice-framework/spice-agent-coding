package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
	agentprocess "github.com/spice-framework/spice-agent/process"
	spicebean "github.com/spice-framework/spice/bean"
	spiceconfig "github.com/spice-framework/spice/config"
)

const (
	spiceTool        = "github.com/spice-framework/toolchain/cmd/spice"
	daemonTestSecret = "daemon-generated-test-secret"
	daemonTestModel  = "daemon-test-model"
)

func TestDaemonGenerationAndBeanExplanationAreCurrent(t *testing.T) {
	root := repositoryRoot(t)
	for _, retired := range []string{
		filepath.Join(root, ".spice", "daemon.manifest.json"),
		filepath.Join(root, "internal", "spicegen", "daemon"),
	} {
		if _, err := os.Stat(retired); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retired daemon generation path %q still exists: %v", retired, err)
		}
	}
	for _, arguments := range [][]string{
		{"generate", "--check", "--target", "spice-agentd", "./cmd/spice-agentd"},
		{"generate", "--diff", "--target", "spice-agentd", "./cmd/spice-agentd"},
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
		`"name": "processLauncher"`,
		`"name": "processResolver"`,
		`"name": "openAIModelProvider"`,
		`"name": "read"`,
		`"name": "replace"`,
		`"name": "shell"`,
		`"name": "runtimePluginCompiledDispatcher"`,
		`"name": "runtimePluginEndpointFactory"`,
		`"name": "runtimePluginHost"`,
		`"name": "runtimePluginToolPlanSource"`,
		`"module": "github.com/spice-framework/spice-agent"`,
		`"name": "daemonRuntime"`,
	} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("bean explanation lacks %q: %s", expected, stdout)
		}
	}
}

func TestGeneratedDaemonConstructsInspectableGraphWithoutPublication(t *testing.T) {
	t.Parallel()
	var pluginLaunches atomic.Int32
	launcher := agentprocess.LauncherFunc(func(context.Context, agentprocess.Spec) (agentprocess.Process, error) {
		pluginLaunches.Add(1)
		return nil, errors.New("generated construction must not launch a runtime plugin")
	})
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
		spicegen.ApplicationOptions{
			Sources: []spiceconfig.Source{values},
			Overrides: spicegen.BeanOverrides{
				ProcessLauncher: spicebean.Replace[agentprocess.Launcher](launcher),
			},
		},
	)
	if err != nil {
		t.Fatalf("construct generated daemon: %v", err)
	}
	components := application.Components()
	if components.DaemonRootRegistry == nil || components.DaemonRoot == nil ||
		components.DaemonEngine == nil || components.RunHost == nil ||
		components.GrpcServer == nil || components.DaemonRuntime == nil ||
		components.ProcessLauncher == nil || components.ProcessResolver == nil ||
		components.Read == nil || components.Replace == nil || components.Shell == nil ||
		components.RuntimePluginHostIdentity == nil || components.RuntimePluginEndpointFactory == nil ||
		components.RuntimePluginCompiledDispatcher == nil || components.RuntimePluginHost == nil ||
		components.RuntimePluginToolPlanSource == nil {
		t.Fatal("generated daemon graph is incomplete")
	}
	sourceHost, ok := components.RuntimePluginToolPlanSource.(*pluginhost.Host)
	if !ok || sourceHost != components.RuntimePluginHost {
		t.Fatalf(
			"runtime plugin plan source = %T %p, want exact generated host %p",
			components.RuntimePluginToolPlanSource,
			sourceHost,
			components.RuntimePluginHost,
		)
	}
	definitions := components.RuntimePluginCompiledDispatcher.Definitions()
	if len(definitions) != 3 || definitions[0].Name() != "read" ||
		definitions[1].Name() != "replace" || definitions[2].Name() != "shell" {
		t.Fatalf("compiled runtime plugin definitions = %#v", definitions)
	}
	if pluginLaunches.Load() != 0 {
		t.Fatalf("runtime plugin launches during construction = %d", pluginLaunches.Load())
	}
	if components.Properties.Model != daemonTestModel || components.OpenAIConfig.APIKey != daemonTestSecret {
		t.Fatal("generated typed configuration was not injected")
	}
	stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = application.Stop(stopContext); err != nil {
		t.Fatalf("stop generated daemon: %v", err)
	}
	if pluginLaunches.Load() != 0 {
		t.Fatalf("runtime plugin launches after generated cleanup = %d", pluginLaunches.Load())
	}
}

func TestGeneratedDaemonIsDirectAndContainmentAdoptionPrecedesChildCapableBeans(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	providers, err := os.ReadFile(filepath.Join(root, "internal", "spicegen", "spice_agentd", "spice_providers_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	registry := bytes.Index(providers, []byte("ConstructDaemonRootRegistry"))
	launcher := bytes.Index(providers, []byte("ConstructProcessLauncher"))
	codingConfig := bytes.Index(providers, []byte("ConstructCodingConfig"))
	shellTool := bytes.Index(providers, []byte("ConstructShell"))
	runtimeHost := bytes.Index(providers, []byte("ConstructRuntimePluginHost_"))
	runtimePlanSource := bytes.Index(providers, []byte("ConstructRuntimePluginToolPlanSource_"))
	if registry < 0 || launcher <= registry || codingConfig <= registry || shellTool <= launcher ||
		runtimeHost <= shellTool || runtimePlanSource <= runtimeHost {
		t.Fatalf(
			"generated containment order registry=%d launcher=%d coding=%d shell=%d host=%d plan=%d",
			registry,
			launcher,
			codingConfig,
			shellTool,
			runtimeHost,
			runtimePlanSource,
		)
	}
	for _, expected := range []string{
		"ConstructDaemonRuntime", "ConstructGrpcServer", "ConstructRunHost",
		"ConstructProcessLauncher", "ConstructProcessResolver",
		"ConstructRuntimePluginHost_", "ConstructRuntimePluginToolPlanSource_",
		`map[string]tool.Tool{"read": read, "replace": replace, "shell": shell}`,
	} {
		if !bytes.Contains(providers, []byte(expected)) {
			t.Fatalf("generated provider file lacks %q", expected)
		}
	}
	for _, forbidden := range []string{
		"reflect.", "RuntimeGraph", "ServiceLocator", "ExtensionRegistry", daemonTestSecret,
	} {
		if bytes.Contains(providers, []byte(forbidden)) {
			t.Fatalf("generated provider file contains forbidden %q", forbidden)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(root, ".spice", "spice_agentd.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"id": "spice_agentd"`,
		`"layout": "application-package"`,
		`"entrypoint_package": "github.com/spice-framework/spice-agent-coding/cmd/spice-agentd"`,
		`"bridge_dir": "cmd/spice-agentd"`, `"role": "source-unit"`,
		"cmd/spice-agentd/application.go",
		"internal/daemon/root.go", "internal/daemon/process.go",
		"NewRootRegistry", "NewProcessLauncher", "NewExecutableResolver", "NewRuntime",
		"NewRuntimePluginHostIdentity",
		"github.com/spice-framework/spice-agent-tools-coding/autoconfigure/autoconfigure.go",
		"github.com/spice-framework/spice-agent/plugin/host/autoconfigure/autoconfigure.go",
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
