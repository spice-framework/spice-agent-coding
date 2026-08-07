package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	agentprocess "github.com/spice-framework/spice-agent/process"
	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
)

func TestRuntimePluginPlanDisabledAndConfiguredContracts(t *testing.T) {
	t.Parallel()
	disabled, err := NewRuntimePluginPlan(RuntimePluginProperties{})
	if err != nil {
		t.Fatalf("disabled plan: %v", err)
	}
	if err = disabled.Validate(); err != nil || disabled.Enabled() || disabled.Required() || disabled.Set().Len() != 0 {
		t.Fatalf("disabled plan = %#v, validation = %v", disabled, err)
	}
	generatedDefaults, err := NewRuntimePluginPlan(RuntimePluginProperties{
		ID: defaultRuntimePluginID, StartupTimeout: defaultRuntimePluginStartupTimeout,
		CallTimeout: defaultRuntimePluginCallTimeout, DrainTimeout: defaultRuntimePluginDrainTimeout,
		ShutdownTimeout:    defaultRuntimePluginShutdownTimeout,
		ContainmentTimeout: defaultRuntimePluginContainmentTimeout,
	})
	if err != nil || generatedDefaults.Enabled() || generatedDefaults.Set().Len() != 0 {
		t.Fatalf("generated-default plan = %#v, error = %v; want disabled", generatedDefaults, err)
	}

	properties := validRuntimePluginProperties(t)
	properties.WorkingDirectory = ""
	properties.Required = true
	properties.EnvironmentRead = true
	properties.EnvironmentWrite = true
	properties.FilesystemRead = true
	properties.FilesystemWrite = true
	properties.NetworkAccess = true
	properties.ProcessExecute = true
	properties.SecretsRead = true
	configured, err := NewRuntimePluginPlan(properties)
	if err != nil {
		t.Fatalf("configured plan: %v", err)
	}
	if err = configured.Validate(); err != nil || !configured.Enabled() || !configured.Required() || configured.Set().Len() != 1 {
		t.Fatalf("configured plan validation = %v", err)
	}
	executable := configured.Set().Executables()[0]
	if executable.Path() != properties.Path || executable.WorkingDirectory() != filepath.Dir(properties.Path) ||
		executable.ManifestName() != properties.ManifestName ||
		executable.ManifestVersion() != properties.ManifestVersion || len(executable.Environment()) != 0 {
		t.Fatal("configured executable did not preserve its explicit non-secret contract")
	}
	wantCapabilities := []tool.Capability{
		tool.CapabilityEnvironmentRead,
		tool.CapabilityEnvironmentWrite,
		tool.CapabilityFilesystemRead,
		tool.CapabilityFilesystemWrite,
		tool.CapabilityNetworkAccess,
		tool.CapabilityProcessExecute,
		tool.CapabilitySecretsRead,
	}
	if got := executable.ApprovedCapabilities(); !slices.Equal(got, wantCapabilities) {
		t.Fatalf("capability order = %v, want %v", got, wantCapabilities)
	}
	limits := executable.RequestedLimits()
	if err = pluginv1.ValidateLimits(limits); err != nil || limits.GetMaxTools() != 256 ||
		limits.GetMaxConcurrentCalls() != 32 {
		t.Fatalf("fixed limits = %#v, validation = %v", limits, err)
	}
	encoded, err := json.Marshal(configured)
	if err != nil {
		t.Fatalf("marshal configured plan: %v", err)
	}
	for _, formatted := range []string{
		fmt.Sprint(configured),
		fmt.Sprintf("%#v", configured),
		string(encoded),
	} {
		for _, sensitive := range []string{properties.Path, properties.SHA256} {
			if strings.Contains(formatted, sensitive) {
				t.Fatalf("formatted plan reflected configured value: %q", formatted)
			}
		}
	}
}

func TestRuntimePluginPlanRejectsPartialAndNonCanonicalConfigurationWithoutReflection(t *testing.T) {
	t.Parallel()
	valid := validRuntimePluginProperties(t)
	tests := map[string]RuntimePluginProperties{
		"required only":        {Required: true},
		"identity only":        {ID: "fixture"},
		"timeout only":         {StartupTimeout: time.Second},
		"relative path":        replaceRuntimePluginProperties(valid, func(value *RuntimePluginProperties) { value.Path = "relative" }),
		"relative working dir": replaceRuntimePluginProperties(valid, func(value *RuntimePluginProperties) { value.WorkingDirectory = "relative" }),
		"uppercase digest":     replaceRuntimePluginProperties(valid, func(value *RuntimePluginProperties) { value.SHA256 = strings.Repeat("A", 64) }),
		"missing timeout":      replaceRuntimePluginProperties(valid, func(value *RuntimePluginProperties) { value.CallTimeout = 0 }),
		"unbounded timeout": replaceRuntimePluginProperties(valid, func(value *RuntimePluginProperties) {
			value.CallTimeout = pluginhost.MaximumOperationTimeout + time.Nanosecond
		}),
		"invalid manifest": replaceRuntimePluginProperties(valid, func(value *RuntimePluginProperties) { value.ManifestName = "bad\nname" }),
	}
	for name, properties := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			plan, err := NewRuntimePluginPlan(properties)
			if err == nil || plan.Enabled() {
				t.Fatalf("NewRuntimePluginPlan() = %#v, %v; want error", plan, err)
			}
			for _, sensitive := range []string{properties.Path, properties.SHA256, properties.ManifestName} {
				if sensitive != "" && strings.Contains(err.Error(), sensitive) {
					t.Fatalf("validation error reflected configured value: %q", err)
				}
			}
		})
	}
}

func TestRuntimePluginRestartPolicyIsExplicitAndBounded(t *testing.T) {
	t.Parallel()
	disabled, err := NewRuntimePluginPlan(RuntimePluginProperties{})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewRuntimePluginRestartPolicy(disabled)
	if err != nil || policy.Enabled() {
		t.Fatalf("disabled runtime plugin restart policy = %#v, error = %v", policy, err)
	}
	enabled, err := NewRuntimePluginPlan(validRuntimePluginProperties(t))
	if err != nil {
		t.Fatal(err)
	}
	policy, err = NewRuntimePluginRestartPolicy(enabled)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.Validate(); err != nil || !policy.Enabled() ||
		policy.MaximumAttempts() != 3 || policy.InitialBackoff() != 250*time.Millisecond ||
		policy.MaximumBackoff() != time.Second || policy.AttemptTimeout() != 30*time.Second {
		t.Fatalf("runtime plugin restart policy = %#v, validation = %v", policy, err)
	}
}

func TestRuntimePluginActivationPreservesCallerCancellation(t *testing.T) {
	t.Parallel()
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprintf("required=%t", required), func(t *testing.T) {
			t.Parallel()
			host := runtimePluginTestHost(t, &atomic.Int32{})
			properties := validRuntimePluginProperties(t)
			properties.Required = required
			plan, err := NewRuntimePluginPlan(properties)
			if err != nil {
				t.Fatal(err)
			}
			activation, err := NewRuntimePluginActivation(plan, host)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err = activation.Start(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("Start() error = %v; want context cancellation", err)
			}
		})
	}
}

func TestRuntimePluginActivationFailurePolicyAndBoundedHealth(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		required   bool
		startError error
		gateError  error
		reason     agentdaemon.HealthReasonCode
	}{
		{
			name: "optional", reason: agentdaemon.HealthReasonDependencyDegraded,
		},
		{
			name: "required", required: true,
			startError: ErrRuntimePluginRequiredUnavailable,
			gateError:  ErrRuntimePluginRequiredUnavailable,
			reason:     agentdaemon.HealthReasonDependencyUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var launches atomic.Int32
			host := runtimePluginTestHost(t, &launches)
			properties := validRuntimePluginProperties(t)
			properties.Path = filepath.Join(t.TempDir(), executableName("missing-plugin"))
			properties.WorkingDirectory = filepath.Dir(properties.Path)
			properties.Required = test.required
			plan, err := NewRuntimePluginPlan(properties)
			if err != nil {
				t.Fatal(err)
			}
			activation, err := NewRuntimePluginActivation(plan, host)
			if err != nil {
				t.Fatal(err)
			}
			startErr := activation.Start(t.Context())
			if test.startError == nil && startErr != nil ||
				test.startError != nil && !errors.Is(startErr, test.startError) {
				t.Fatalf("Start() error = %v, want %v", startErr, test.startError)
			}
			if gateErr := activation.PublicationReady(); test.gateError == nil && gateErr != nil ||
				test.gateError != nil && !errors.Is(gateErr, test.gateError) {
				t.Fatalf("PublicationReady() error = %v, want %v", gateErr, test.gateError)
			}
			if launches.Load() != 0 {
				t.Fatalf("failed pre-launch verification started %d processes", launches.Load())
			}
			if !test.required {
				lease, leaseErr := host.LeaseCurrent(t.Context())
				if leaseErr != nil {
					t.Fatalf("lease compiled fallback generation: %v", leaseErr)
				}
				definitions := lease.Definitions()
				if len(definitions) != 1 || definitions[0].Name() != "compiled.fixture" {
					t.Fatalf("optional failure changed compiled definitions: %#v", definitions)
				}
				if releaseErr := lease.Release(); releaseErr != nil {
					t.Fatalf("release compiled fallback generation: %v", releaseErr)
				}
			}
			source, err := NewRuntimePluginHealthSource(activation, host)
			if err != nil {
				t.Fatal(err)
			}
			contribution := source.HealthContribution()
			if err = contribution.Validate(); err != nil ||
				!slices.Equal(contribution.Reasons(), []agentdaemon.HealthReasonCode{test.reason}) {
				t.Fatalf("health = %v, validation = %v", contribution.Reasons(), err)
			}
			for _, sensitive := range []string{properties.Path, properties.SHA256} {
				if startErr != nil && strings.Contains(startErr.Error(), sensitive) {
					t.Fatalf("activation reflected configured value: %q", startErr)
				}
			}
		})
	}
}

func TestRuntimePluginDisabledActivationStartsNoProcessAndReportsReady(t *testing.T) {
	t.Parallel()
	var launches atomic.Int32
	host := runtimePluginTestHost(t, &launches)
	plan, err := NewRuntimePluginPlan(RuntimePluginProperties{})
	if err != nil {
		t.Fatal(err)
	}
	activation, err := NewRuntimePluginActivation(plan, host)
	if err != nil {
		t.Fatal(err)
	}
	if activation.host != host {
		t.Fatal("runtime plugin activation did not retain the exact generated Host pointer")
	}
	if err = activation.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err = activation.PublicationReady(); err != nil {
		t.Fatalf("PublicationReady() error = %v", err)
	}
	if err = activation.Start(t.Context()); !errors.Is(err, ErrRuntimePluginActivationPending) {
		t.Fatalf("second Start() error = %v", err)
	}
	if launches.Load() != 0 {
		t.Fatalf("disabled activation launched %d processes", launches.Load())
	}
	source, err := NewRuntimePluginHealthSource(activation, host)
	if err != nil || len(source.HealthContribution().Reasons()) != 0 {
		t.Fatalf("disabled health source = %v, error = %v", source, err)
	}
	typed, ok := source.(*runtimePluginHealthSource)
	if !ok || typed.host != host || typed.activation != activation {
		t.Fatal("runtime plugin health source did not retain exact generated dependencies")
	}
	if source, err = NewRuntimePluginHealthSource(nil, host); err == nil || source != nil {
		t.Fatal("runtime plugin health source accepted nil activation")
	}
}

func TestRuntimePublicationGateRunsBeforeListening(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		activation *RuntimePluginActivation
		want       error
	}{
		{name: "pending", activation: &RuntimePluginActivation{}, want: ErrRuntimePluginActivationPending},
		{
			name:       "required failure",
			activation: &RuntimePluginActivation{state: runtimePluginActivationFailed},
			want:       ErrRuntimePluginRequiredUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var listens atomic.Int32
			runtime := &Runtime{
				activation: test.activation, server: immediateServer{}, serveDone: make(chan struct{}),
				services: runtimeServices{
					listen: func(string) (net.Listener, error) {
						listens.Add(1)
						return idleListener{}, nil
					},
				},
			}
			if err := runtime.Start(t.Context()); !errors.Is(err, test.want) {
				t.Fatalf("Start() error = %v, want %v", err, test.want)
			}
			if listens.Load() != 0 {
				t.Fatalf("publication gate opened %d listeners", listens.Load())
			}
		})
	}
}

func runtimePluginTestHost(t *testing.T, launches *atomic.Int32) *pluginhost.Host {
	t.Helper()
	definition, err := tool.NewDefinition(
		"compiled.fixture", "Compiled fallback fixture.", []byte(`{"type":"object"}`),
		tool.EffectReadOnly, tool.ReplaySafe,
	)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := stage.NewDispatcher(map[string]tool.Tool{
		"compiled.fixture": compiledRuntimePluginTool{definition: definition},
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := pluginhost.NewHost(pluginhost.HostConfig{
		HostIdentity: &pluginv1.BuildIdentity{
			Component: "spice-agentd-test", Version: "v1",
			Commit: "fixture", Runtime: runtime.Version(),
		},
		Compiled: dispatcher,
		Processes: agentprocess.LauncherFunc(func(context.Context, agentprocess.Spec) (agentprocess.Process, error) {
			launches.Add(1)
			return nil, errors.New("test launcher must not start")
		}),
		Endpoints: unreachableEndpointFactory{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if closeErr := host.Close(ctx); closeErr != nil {
			t.Errorf("close runtime plugin test host: %v", closeErr)
		}
	})
	return host
}

type compiledRuntimePluginTool struct{ definition tool.Definition }

func (fixture compiledRuntimePluginTool) Definition() tool.Definition { return fixture.definition }

func (compiledRuntimePluginTool) Execute(
	context.Context,
	tool.Call,
	tool.Reporter,
) (tool.Result, error) {
	return tool.Result{}, errors.New("compiled fixture execution is unavailable")
}

type unreachableEndpointFactory struct{}

func (unreachableEndpointFactory) Open(context.Context, string) (pluginhost.LocalEndpoint, error) {
	return nil, errors.New("test endpoint must not open")
}

func validRuntimePluginProperties(t *testing.T) RuntimePluginProperties {
	t.Helper()
	path := filepath.Join(t.TempDir(), executableName("fixture-plugin"))
	return RuntimePluginProperties{
		ID: "fixture", Path: path, SHA256: strings.Repeat("a", 64),
		ManifestName: "fixture", ManifestVersion: "v1",
		WorkingDirectory: filepath.Dir(path), StartupTimeout: time.Second,
		CallTimeout: time.Second, DrainTimeout: time.Second,
		ShutdownTimeout: time.Second, ContainmentTimeout: time.Second,
	}
}

func replaceRuntimePluginProperties(
	value RuntimePluginProperties,
	replace func(*RuntimePluginProperties),
) RuntimePluginProperties {
	replace(&value)
	return value
}

func executableName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}
