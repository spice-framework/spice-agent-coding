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
		`"name": "runtimePluginRestartPolicy"`,
		`"name": "runtimePluginHost"`,
		`"name": "runtimePluginToolPlanSource"`,
		`"name": "runtimePluginPlan"`,
		`"name": "runtimePluginActivation"`,
		`"name": "runtimePluginHealthSource"`,
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
		components.RuntimePluginToolPlanSource == nil || components.RuntimePluginActivation == nil ||
		components.RuntimePluginHealthSource == nil {
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
	health := components.RuntimePluginHost.Health()
	if err = health.Validate(); err != nil {
		t.Fatalf("initial runtime plugin host health: %v", err)
	}
	if health.State() != pluginhost.HealthStateReady || health.RestartLimit() != 0 ||
		health.RestartAttempts() != 0 || len(health.Issues()) != 0 {
		t.Fatalf(
			"initial runtime plugin host health = %s, want ready with disabled recovery",
			health,
		)
	}
	if components.Properties.Model != daemonTestModel || components.OpenAIConfig.APIKey != daemonTestSecret {
		t.Fatal("generated typed configuration was not injected")
	}
	if err = components.RuntimePluginRestartPolicy.Validate(); err != nil ||
		components.RuntimePluginRestartPolicy.Enabled() {
		t.Fatalf("generated disabled runtime plugin restart policy is invalid: %v", err)
	}
	if err = components.RuntimePluginPlan.Validate(); err != nil || components.RuntimePluginPlan.Enabled() {
		t.Fatalf("generated disabled runtime plugin plan is invalid: %v", err)
	}
	properties := components.RuntimePluginProperties
	if properties.ID != "runtime-tool" || properties.StartupTimeout != 10*time.Second ||
		properties.CallTimeout != 2*time.Minute || properties.DrainTimeout != 10*time.Second ||
		properties.ShutdownTimeout != 10*time.Second || properties.ContainmentTimeout != 5*time.Second {
		t.Fatalf("generated runtime plugin defaults = %#v", properties)
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

func TestGeneratedDaemonRejectsPartialRuntimePluginConfigurationWithoutLaunch(t *testing.T) {
	t.Parallel()
	var pluginLaunches atomic.Int32
	launcher := agentprocess.LauncherFunc(func(context.Context, agentprocess.Spec) (agentprocess.Process, error) {
		pluginLaunches.Add(1)
		return nil, errors.New("partial configuration must not launch a runtime plugin")
	})
	values, err := spiceconfig.NewMapSource("test", map[string]string{
		"agent.openai.api-key":          daemonTestSecret,
		"agent.model":                   daemonTestModel,
		"agent.workspace":               t.TempDir(),
		"agent.runtime-plugin.required": "true",
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
	if err == nil || application != nil {
		t.Fatalf("partial runtime plugin construction = %p, %v; want nil, error", application, err)
	}
	if pluginLaunches.Load() != 0 {
		t.Fatalf("partial runtime plugin configuration launched %d processes", pluginLaunches.Load())
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
	runtimePlan := bytes.Index(providers, []byte("ConstructRuntimePluginPlan"))
	runtimeRestart := bytes.Index(providers, []byte("ConstructRuntimePluginRestartPolicy"))
	runtimeActivation := bytes.Index(providers, []byte("ConstructRuntimePluginActivation"))
	runtimeHealth := bytes.Index(providers, []byte("ConstructRuntimePluginHealthSource"))
	if registry < 0 || launcher <= registry || codingConfig <= registry || shellTool <= launcher ||
		runtimePlan <= shellTool || runtimeRestart <= runtimePlan || runtimeHost <= runtimeRestart ||
		runtimePlanSource <= runtimeHost ||
		runtimeActivation <= runtimeHost || runtimeHealth <= runtimeActivation {
		t.Fatalf(
			"generated containment order registry=%d launcher=%d coding=%d shell=%d config-plan=%d restart=%d host=%d source=%d activation=%d health=%d",
			registry,
			launcher,
			codingConfig,
			shellTool,
			runtimePlan,
			runtimeRestart,
			runtimeHost,
			runtimePlanSource,
			runtimeActivation,
			runtimeHealth,
		)
	}
	for _, expected := range []string{
		"ConstructDaemonRuntime", "ConstructGrpcServer", "ConstructRunHost",
		"ConstructProcessLauncher", "ConstructProcessResolver",
		"ConstructRuntimePluginHost_", "ConstructRuntimePluginToolPlanSource_",
		"ConstructRuntimePluginRestartPolicy", "ConstructRuntimePluginPlan",
		"ConstructRuntimePluginActivation", "ConstructRuntimePluginHealthSource",
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
		"NewRuntimePluginHostIdentity", "NewRuntimePluginRestartPolicy",
		"NewRuntimePluginPlan", "NewRuntimePluginActivation", "NewRuntimePluginHealthSource",
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
	configuration, err := os.ReadFile(filepath.Join(root, "internal", "spicegen", "spice_agentd", "spice_configuration_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"agent.runtime-plugin.required", "agent.runtime-plugin.id",
		"agent.runtime-plugin.path", "agent.runtime-plugin.sha256",
		"agent.runtime-plugin.manifest-name", "agent.runtime-plugin.manifest-version",
		"agent.runtime-plugin.working-directory",
		"agent.runtime-plugin.capabilities.filesystem-read",
		"agent.runtime-plugin.capabilities.filesystem-write",
		"agent.runtime-plugin.capabilities.process-execute",
		"agent.runtime-plugin.capabilities.network-access",
		"agent.runtime-plugin.capabilities.secrets-read",
		"agent.runtime-plugin.capabilities.environment-read",
		"agent.runtime-plugin.capabilities.environment-write",
		"agent.runtime-plugin.timeouts.startup", "agent.runtime-plugin.timeouts.call",
		"agent.runtime-plugin.timeouts.drain", "agent.runtime-plugin.timeouts.shutdown",
		"agent.runtime-plugin.timeouts.containment",
	} {
		if !bytes.Contains(configuration, []byte(key)) {
			t.Fatalf("generated configuration lacks public key %q", key)
		}
	}
	lifecycle, err := os.ReadFile(filepath.Join(root, "internal", "spicegen", "spice_agentd", "spice_features_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	activationStart := bytes.Index(lifecycle, []byte("runtimePluginActivation.Start"))
	runtimeStart := bytes.Index(lifecycle, []byte("daemonRuntime.Start"))
	if activationStart < 0 || runtimeStart <= activationStart {
		t.Fatalf("generated lifecycle order activation=%d runtime=%d", activationStart, runtimeStart)
	}
}

func runSpice(t *testing.T, root string, arguments ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
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
