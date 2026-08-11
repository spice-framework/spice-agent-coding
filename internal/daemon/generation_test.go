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
	"github.com/spice-framework/spice-agent-coding/internal/testpath"
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

func TestDaemonGenerationIsCurrent(t *testing.T) {
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
}

func TestGeneratedDaemonConstructsInspectableGraphWithoutPublication(t *testing.T) {
	t.Parallel()
	var pluginLaunches atomic.Int32
	launcher := agentprocess.LauncherFunc(func(context.Context, agentprocess.Spec) (agentprocess.Process, error) {
		pluginLaunches.Add(1)
		return nil, errors.New("generated construction must not launch a runtime plugin")
	})
	verified := agentprocess.VerifiedLauncherFunc(func(context.Context, *agentprocess.ExecutableLease, agentprocess.Spec) (agentprocess.Process, error) {
		pluginLaunches.Add(1)
		return nil, errors.New("generated construction must not launch a runtime plugin")
	})
	workspace := t.TempDir()
	values, err := spiceconfig.NewMapSource("test", map[string]string{
		"agent.openai.api-key": daemonTestSecret,
		"agent.model":          daemonTestModel,
		"agent.workspace":      workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := spicegen.NewApplicationWithOptions(
		context.Background(),
		spicegen.ApplicationOptions{
			Sources: []spiceconfig.Source{values},
			Overrides: spicegen.BeanOverrides{
				ProcessLauncher:         spicebean.Replace[agentprocess.Launcher](launcher),
				VerifiedProcessLauncher: spicebean.Replace[agentprocess.VerifiedLauncher](verified),
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
		components.RuntimePluginHealthSource == nil || components.VerifiedProcessLauncher == nil ||
		components.AgentLoggingMailbox == nil || components.AgentLoggingProcessor == nil ||
		components.AgentLoggingHealth == nil {
		t.Fatal("generated daemon graph is incomplete")
	}
	if components.AgentLoggingConfig.MailboxCapacity != 1024 ||
		components.AgentLoggingConfig.IncludeProgress || components.AgentLoggingConfig.ReadinessImpact {
		t.Fatalf("generated Agent logging defaults = %#v", components.AgentLoggingConfig)
	}
	if contribution := components.AgentLoggingHealth.HealthContribution(); len(contribution.Reasons()) != 0 {
		t.Fatalf("default Agent logging readiness contribution = %v", contribution)
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
	if components.DaemonProperties.Model != daemonTestModel ||
		components.WorkspaceProperties.Workspace != workspace ||
		components.OpenAIConfig.APIKey != daemonTestSecret {
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
	verified := agentprocess.VerifiedLauncherFunc(func(context.Context, *agentprocess.ExecutableLease, agentprocess.Spec) (agentprocess.Process, error) {
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
				ProcessLauncher:         spicebean.Replace[agentprocess.Launcher](launcher),
				VerifiedProcessLauncher: spicebean.Replace[agentprocess.VerifiedLauncher](verified),
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
	loggingConfig := bytes.Index(providers, []byte("ConstructAgentLoggingConfig"))
	loggingMailbox := bytes.Index(providers, []byte("ConstructAgentLoggingMailbox"))
	loggingProcessor := bytes.Index(providers, []byte("ConstructAgentLoggingProcessor"))
	loggingHealth := bytes.Index(providers, []byte("ConstructAgentLoggingHealth"))
	daemonEngine := bytes.Index(providers, []byte("ConstructDaemonEngine"))
	if registry < 0 || launcher <= registry || codingConfig <= registry || shellTool <= launcher ||
		runtimePlan <= registry || runtimeRestart <= runtimePlan || runtimeHost <= runtimeRestart ||
		runtimeHost <= shellTool ||
		runtimePlanSource <= runtimeHost ||
		runtimeActivation <= runtimeHost || runtimeHealth <= runtimeActivation ||
		loggingConfig < 0 || loggingMailbox <= loggingConfig || loggingProcessor <= loggingMailbox ||
		loggingHealth <= loggingProcessor || daemonEngine <= loggingProcessor {
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
	if !bytes.Contains(providers, []byte("[]*event.BestEffortObserver{agentLoggingMailbox}")) ||
		!bytes.Contains(providers, []byte("[]daemon2.HealthSource{agentLoggingHealth, runtimePluginHealthSource}")) {
		t.Fatal("generated Agent logging observer or health collection is incomplete or unordered")
	}
	for _, expected := range []string{
		"ConstructDaemonRuntime", "ConstructGrpcServer", "ConstructRunHost",
		"ConstructProcessLauncher", "ConstructProcessResolver",
		"ConstructRuntimePluginHost_", "ConstructRuntimePluginToolPlanSource_",
		"ConstructRuntimePluginRestartPolicy", "ConstructRuntimePluginPlan",
		"ConstructRuntimePluginActivation", "ConstructRuntimePluginHealthSource",
		"ConstructAgentLoggingConfig", "ConstructAgentLoggingMailbox",
		"ConstructAgentLoggingProcessor", "ConstructAgentLoggingHealth",
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
		"internal/daemon/daemon_root_registry_bean.go",
		"internal/daemon/process_launcher_bean.go",
		"NewRootRegistry", "NewProcessLauncher", "NewExecutableResolver", "NewRuntime",
		"NewRuntimePluginHostIdentity", "NewRuntimePluginRestartPolicy",
		"NewRuntimePluginPlan", "NewRuntimePluginActivation", "NewRuntimePluginHealthSource",
		"NewAgentLoggingConfig", "NewAgentLoggingHealthSource",
		"github.com/spice-framework/spice-agent/logging/autoconfigure/autoconfigure.go",
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
		"agent.logging.mailbox-capacity", "agent.logging.include-progress",
		"agent.logging.readiness-impact",
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
	command.Env = testpath.NewSupport().Environment(map[string]string{
		"GOFLAGS": "-mod=vendor", "GOPROXY": "off", "GOTOOLCHAIN": "local", "GOWORK": "off",
	})
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
