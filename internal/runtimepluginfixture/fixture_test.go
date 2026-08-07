package runtimepluginfixture_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	distributiondaemon "github.com/spice-framework/spice-agent-coding/internal/daemon"
	"github.com/spice-framework/spice-agent-coding/internal/processplatform"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
	"github.com/spice-framework/spice-agent/plugin/host/localendpoint"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
	spiceconfig "github.com/spice-framework/spice/config"
	spicelifecycle "github.com/spice-framework/spice/lifecycle"
)

const (
	fixtureManifest = "spice-agent-distribution-fixture"
	fixtureVersion  = "v1"
	fixtureBlock    = "fixture.block"
	fixtureTool     = "fixture.echo"
)

func TestOfflineFixtureActivatesAndShutsDownThroughProductionHost(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	executablePath, digest := buildFixture(t, root)

	launcher, err := processplatform.NewLauncher(acceptingRegistrar{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := stage.NewDispatcher(map[string]tool.Tool{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := distributiondaemon.NewRuntimePluginPlan(distributiondaemon.RuntimePluginProperties{
		Required: true, ID: "distribution-fixture", Path: executablePath,
		SHA256: digest, ManifestName: fixtureManifest, ManifestVersion: fixtureVersion,
		StartupTimeout: 5 * time.Second, CallTimeout: 5 * time.Second,
		DrainTimeout: 5 * time.Second, ShutdownTimeout: 5 * time.Second,
		ContainmentTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("construct fixture activation plan: %v", err)
	}
	restart, err := distributiondaemon.NewRuntimePluginRestartPolicy(plan)
	if err != nil {
		t.Fatalf("construct fixture restart policy: %v", err)
	}
	host, err := pluginhost.NewHost(pluginhost.HostConfig{
		HostIdentity: &pluginv1.BuildIdentity{
			Component: "spice-agentd-fixture-host", Version: "v1",
			Commit: "fixture", Runtime: runtime.Version(),
		},
		Compiled: compiled, Processes: launcher, Endpoints: localendpoint.NewFactory(),
		Restart: restart,
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := host.Close(ctx); closeErr != nil {
			t.Errorf("close fixture host during cleanup: %v", closeErr)
		}
	})

	activation, err := distributiondaemon.NewRuntimePluginActivation(plan, host)
	if err != nil {
		t.Fatal(err)
	}
	activationContext, cancelActivation := context.WithTimeout(t.Context(), 10*time.Second)
	err = activation.Start(activationContext)
	cancelActivation()
	if err != nil {
		t.Fatalf("activate fixture: %v", err)
	}
	if err = activation.PublicationReady(); err != nil {
		t.Fatalf("fixture publication gate: %v", err)
	}
	planID := host.Health().CurrentPlanID()
	if err = planID.Validate(); err != nil {
		t.Fatalf("activated plan identity: %v", err)
	}
	healthSource, err := distributiondaemon.NewRuntimePluginHealthSource(activation, host)
	if err != nil || len(healthSource.HealthContribution().Reasons()) != 0 {
		t.Fatalf("activated fixture health source = %v, error = %v", healthSource, err)
	}

	lease, err := host.LeaseCurrent(t.Context())
	if err != nil {
		t.Fatalf("lease fixture generation: %v", err)
	}
	definitions := lease.Definitions()
	if len(definitions) != 2 || definitions[0].Name() != fixtureBlock ||
		definitions[1].Name() != fixtureTool ||
		definitions[1].Effect() != tool.EffectReadOnly ||
		definitions[1].ReplaySafety() != tool.ReplaySafe ||
		len(definitions[1].Capabilities()) != 0 {
		t.Fatalf("fixture definitions = %#v", definitions)
	}
	call, err := tool.NewCall("distribution-fixture-call", fixtureTool, []byte(`{"value":"offline"}`))
	if err != nil {
		t.Fatal(err)
	}
	reporter := &recordingReporter{}
	result, err := lease.Dispatcher().Dispatch(t.Context(), call, reporter)
	if err != nil {
		t.Fatalf("dispatch fixture echo: %v", err)
	}
	if string(result.Content()) != `{"value":"offline"}` {
		t.Fatalf("fixture result = %s", result.Content())
	}
	if got := reporter.messages(); len(got) != 1 || got[0] != "echo accepted" {
		t.Fatalf("fixture progress = %#v", got)
	}
	if err = lease.Release(); err != nil {
		t.Fatalf("release fixture generation: %v", err)
	}

	closeContext, cancelClose := context.WithTimeout(context.Background(), 10*time.Second)
	err = host.Close(closeContext)
	cancelClose()
	if err != nil {
		t.Fatalf("drain and shut down fixture: %v", err)
	}
	closed = true
	health := host.Health()
	if err = health.Validate(); err != nil || health.State() != pluginhost.HealthStateStopped ||
		health.ActiveLeases() != 0 || health.RetainedGenerations() != 0 {
		t.Fatalf("closed fixture host health = %s, validation = %v", health, err)
	}
}

func TestGeneratedDaemonLifecycleActivatesConfiguredFixtureBeforePublication(t *testing.T) {
	root := repositoryRoot(t)
	executablePath, digest := buildFixture(t, root)
	workspace := t.TempDir()
	values, err := spiceconfig.NewMapSource("generated-runtime-plugin-acceptance", map[string]string{
		"agent.openai.api-key":                             "generated-runtime-plugin-test-secret",
		"agent.model":                                      "generated-runtime-plugin-test-model",
		"agent.workspace":                                  workspace,
		"agent.runtime-plugin.required":                    "true",
		"agent.runtime-plugin.id":                          "distribution-fixture",
		"agent.runtime-plugin.path":                        executablePath,
		"agent.runtime-plugin.sha256":                      digest,
		"agent.runtime-plugin.manifest-name":               fixtureManifest,
		"agent.runtime-plugin.manifest-version":            fixtureVersion,
		"agent.runtime-plugin.timeouts.startup":            "5s",
		"agent.runtime-plugin.timeouts.call":               "5s",
		"agent.runtime-plugin.timeouts.drain":              "5s",
		"agent.runtime-plugin.timeouts.shutdown":           "5s",
		"agent.runtime-plugin.timeouts.containment":        "5s",
		"agent.runtime-plugin.capabilities.network-access": "false",
	})
	if err != nil {
		t.Fatal(err)
	}

	var application *spicegen.Application
	startContext, cancelStart := context.WithCancel(t.Context())
	defer cancelStart()
	probe := generatedLifecycleProbe{}
	observer := func(ctx context.Context, observation spicelifecycle.Observation) {
		probe.observe(observation)
		if observation.Operation != spicelifecycle.OperationStart ||
			observation.Phase != spicelifecycle.PhaseEnd || observation.Err != nil ||
			!strings.Contains(observation.Component, "NewRuntimePluginActivation") {
			return
		}
		if application == nil {
			probe.setProbeError(errors.New("generated application is unavailable during activation observation"))
			return
		}
		probe.setProbeError(probeGeneratedRuntimePlugin(ctx, application.Components()))
		cancelStart()
	}
	application, err = spicegen.NewApplicationWithOptions(t.Context(), spicegen.ApplicationOptions{
		Sources:   []spiceconfig.Source{values},
		Observers: []spicelifecycle.Observer{observer},
	})
	if err != nil {
		t.Fatalf("construct generated daemon application: %v", err)
	}
	components := application.Components()
	assertGeneratedRuntimePluginConfiguration(t, components, executablePath, digest)

	startErr := application.Start(startContext)
	if !errors.Is(startErr, context.Canceled) {
		t.Fatalf("generated daemon guarded start error = %v", startErr)
	}
	if application.State() != spicelifecycle.StateFailed {
		t.Fatalf("generated daemon state = %s, want failed rollback", application.State())
	}
	if probeErr := probe.probeError(); probeErr != nil {
		t.Fatalf("probe generated runtime plugin between lifecycle hooks: %v", probeErr)
	}
	probe.assertLifecycleOrder(t)

	health := components.RuntimePluginHost.Health()
	if err = health.Validate(); err != nil || health.State() != pluginhost.HealthStateStopped ||
		health.ActiveLeases() != 0 || health.RetainedGenerations() != 0 {
		t.Fatalf("generated rollback host health = %s, validation = %v", health, err)
	}
}

type generatedLifecycleObservation struct {
	component string
	operation spicelifecycle.Operation
	phase     spicelifecycle.Phase
	failed    bool
}

type generatedLifecycleProbe struct {
	mu           sync.Mutex
	observations []generatedLifecycleObservation
	probed       bool
	err          error
}

func (probe *generatedLifecycleProbe) observe(observation spicelifecycle.Observation) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	probe.observations = append(probe.observations, generatedLifecycleObservation{
		component: observation.Component,
		operation: observation.Operation,
		phase:     observation.Phase,
		failed:    observation.Err != nil,
	})
}

func (probe *generatedLifecycleProbe) setProbeError(err error) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.probed {
		probe.err = errors.Join(probe.err, errors.New("generated activation was probed more than once"))
		return
	}
	probe.probed = true
	probe.err = err
}

func (probe *generatedLifecycleProbe) probeError() error {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if !probe.probed {
		return errors.New("generated activation hook was not probed")
	}
	return probe.err
}

func (probe *generatedLifecycleProbe) assertLifecycleOrder(t *testing.T) {
	t.Helper()
	probe.mu.Lock()
	observations := append([]generatedLifecycleObservation(nil), probe.observations...)
	probe.mu.Unlock()
	activationEnd := generatedObservationIndex(
		observations,
		"NewRuntimePluginActivation",
		spicelifecycle.OperationStart,
		spicelifecycle.PhaseEnd,
	)
	runtimeBegin := generatedObservationIndex(
		observations,
		"|10:NewRuntime",
		spicelifecycle.OperationStart,
		spicelifecycle.PhaseBegin,
	)
	hostCleanup := generatedObservationIndex(
		observations,
		"NewRuntimePluginHost",
		spicelifecycle.OperationCleanup,
		spicelifecycle.PhaseEnd,
	)
	registryCleanup := generatedObservationIndex(
		observations,
		"NewRootRegistry",
		spicelifecycle.OperationCleanup,
		spicelifecycle.PhaseEnd,
	)
	if activationEnd < 0 || runtimeBegin >= 0 ||
		hostCleanup <= activationEnd || registryCleanup <= hostCleanup {
		t.Fatalf(
			"generated lifecycle order activation=%d runtime-begin=%d host-cleanup=%d registry-cleanup=%d: %#v",
			activationEnd,
			runtimeBegin,
			hostCleanup,
			registryCleanup,
			observations,
		)
	}
	if observations[activationEnd].failed || observations[hostCleanup].failed ||
		observations[registryCleanup].failed {
		t.Fatalf("generated lifecycle outcomes = %#v", observations)
	}
}

func generatedObservationIndex(
	observations []generatedLifecycleObservation,
	component string,
	operation spicelifecycle.Operation,
	phase spicelifecycle.Phase,
) int {
	for index, observation := range observations {
		if strings.Contains(observation.component, component) && observation.operation == operation &&
			observation.phase == phase {
			return index
		}
	}
	return -1
}

func assertGeneratedRuntimePluginConfiguration(
	t *testing.T,
	components spicegen.Components,
	executablePath string,
	digest string,
) {
	t.Helper()
	properties := components.RuntimePluginProperties
	if !properties.Required || properties.ID != "distribution-fixture" ||
		properties.Path != executablePath || properties.SHA256 != digest ||
		properties.ManifestName != fixtureManifest || properties.ManifestVersion != fixtureVersion ||
		properties.StartupTimeout != 5*time.Second || properties.CallTimeout != 5*time.Second ||
		properties.DrainTimeout != 5*time.Second || properties.ShutdownTimeout != 5*time.Second ||
		properties.ContainmentTimeout != 5*time.Second {
		t.Fatalf("generated runtime plugin properties = %#v", properties)
	}
	if err := components.RuntimePluginPlan.Validate(); err != nil ||
		!components.RuntimePluginPlan.Enabled() || !components.RuntimePluginPlan.Required() {
		t.Fatalf("generated runtime plugin plan = %s, validation = %v", components.RuntimePluginPlan, err)
	}
	executables := components.RuntimePluginPlan.Set().Executables()
	if len(executables) != 1 || executables[0].ID() != "distribution-fixture" ||
		executables[0].Path() != executablePath || executables[0].SHA256().String() != digest ||
		executables[0].ManifestName() != fixtureManifest ||
		executables[0].ManifestVersion() != fixtureVersion ||
		executables[0].WorkingDirectory() != filepath.Dir(executablePath) ||
		len(executables[0].Environment()) != 0 || len(executables[0].ApprovedCapabilities()) != 0 {
		t.Fatalf("generated runtime plugin executables = %#v", executables)
	}
	restart := components.RuntimePluginRestartPolicy
	if err := restart.Validate(); err != nil || !restart.Enabled() ||
		restart.MaximumAttempts() != 3 || restart.InitialBackoff() != 250*time.Millisecond ||
		restart.MaximumBackoff() != time.Second || restart.AttemptTimeout() != 30*time.Second {
		t.Fatalf("generated runtime plugin restart policy = %#v, validation = %v", restart, err)
	}
	if components.RuntimePluginHost == nil || components.RuntimePluginActivation == nil ||
		components.RuntimePluginHealthSource == nil ||
		components.RuntimePluginToolPlanSource != components.RuntimePluginHost {
		t.Fatal("generated runtime plugin graph is not wired to one exact host")
	}
}

func probeGeneratedRuntimePlugin(ctx context.Context, components spicegen.Components) (resultErr error) {
	health := components.RuntimePluginHost.Health()
	if err := health.Validate(); err != nil {
		return fmt.Errorf("validate activated generated host health: %w", err)
	}
	if health.State() != pluginhost.HealthStateReady || health.RestartLimit() != 3 || health.RestartAttempts() != 0 ||
		health.ActiveLeases() != 0 || health.RetainedGenerations() < 1 {
		return fmt.Errorf("activated generated host health = %s", health)
	}
	if err := components.RuntimePluginActivation.PublicationReady(); err != nil {
		return fmt.Errorf("generated activation publication gate: %w", err)
	}
	contribution := components.RuntimePluginHealthSource.HealthContribution()
	if err := contribution.Validate(); err != nil {
		return fmt.Errorf("validate generated ready health contribution: %w", err)
	}
	if len(contribution.Reasons()) != 0 {
		return fmt.Errorf("generated ready health contribution = %v", contribution)
	}
	lease, err := components.RuntimePluginToolPlanSource.LeaseCurrent(ctx)
	if err != nil {
		return fmt.Errorf("lease generated runtime plugin plan: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, lease.Release())
	}()
	definitions := lease.Definitions()
	if len(definitions) != 5 || definitions[0].Name() != fixtureBlock ||
		definitions[1].Name() != fixtureTool || definitions[2].Name() != "read" ||
		definitions[3].Name() != "replace" || definitions[4].Name() != "shell" {
		return fmt.Errorf("generated runtime plugin definitions = %#v", definitions)
	}
	call, err := tool.NewCall("generated-distribution-fixture-call", fixtureTool, []byte(`{"value":"generated"}`))
	if err != nil {
		return err
	}
	reporter := &recordingReporter{}
	result, err := lease.Dispatcher().Dispatch(ctx, call, reporter)
	if err != nil {
		return fmt.Errorf("dispatch generated fixture echo: %w", err)
	}
	if string(result.Content()) != `{"value":"generated"}` {
		return fmt.Errorf("generated fixture result = %s", result.Content())
	}
	if got := reporter.messages(); len(got) != 1 || got[0] != "echo accepted" {
		return fmt.Errorf("generated fixture progress = %#v", got)
	}
	return nil
}

func TestFixtureSourceUsesOnlyPublicSpiceAgentPackages(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	fixtureRoot := filepath.Join(root, "testdata", "runtimeplugin", "go")
	entries, err := os.ReadDir(fixtureRoot)
	if err != nil {
		t.Fatal(err)
	}
	stdoutReferences := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(fixtureRoot, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		stdoutReferences += bytes.Count(content, []byte("os.Stdout"))
		for _, forbidden := range []string{
			"github.com/spice-framework/spice-agent/internal/",
			"fmt.Print(", "fmt.Printf(", "fmt.Println(", "log.Print",
		} {
			if bytes.Contains(content, []byte(forbidden)) {
				t.Fatalf("fixture source %s contains forbidden %q", entry.Name(), forbidden)
			}
		}
	}
	if stdoutReferences != 1 {
		t.Fatalf("fixture stdout references = %d, want exact readiness writer", stdoutReferences)
	}
	mainSource, err := os.ReadFile(filepath.Join(fixtureRoot, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(mainSource, []byte("pluginv1.WriteReadiness(os.Stdout)")) {
		t.Fatal("fixture stdout is not owned exclusively by the protocol readiness helper")
	}
}

type acceptingRegistrar struct{}

func (acceptingRegistrar) Register(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return errors.New("fixture process registration is invalid")
	}
	return nil
}

type recordingReporter struct {
	mu       sync.Mutex
	progress []string
}

func (reporter *recordingReporter) Report(ctx context.Context, progress tool.Progress) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	reporter.progress = append(reporter.progress, progress.Message())
	return nil
}

func (reporter *recordingReporter) messages() []string {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	return append([]string(nil), reporter.progress...)
}

func buildFixture(t *testing.T, root string) (string, string) {
	t.Helper()
	name := "spice-agent-distribution-fixture"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext( // #nosec G204,G702 -- exact Go and fixed fixture package.
		ctx,
		exactGoExecutable(),
		"build", "-mod=vendor", "-trimpath", "-buildvcs=false",
		"-ldflags=-buildid=", "-o", path, "./testdata/runtimeplugin/go",
	)
	command.Dir = root
	command.Env = append(
		os.Environ(),
		"GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("build offline fixture: stdout %q, stderr %q, error %v", stdout.String(), stderr.String(), err)
	}
	if ctx.Err() != nil {
		t.Fatalf("build offline fixture: %v", ctx.Err())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("fixture build emitted output: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	if digest != strings.ToLower(digest) || len(digest) != sha256.Size*2 {
		t.Fatalf("fixture digest is not canonical lowercase SHA-256: %q", digest)
	}
	return path, digest
}

func exactGoExecutable() string {
	name := "go"
	if runtime.GOOS == "windows" {
		name = "go.exe"
	}
	return filepath.Join(runtime.GOROOT(), "bin", name) //nolint:staticcheck // Select the exact executing toolchain.
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	current, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		content, readErr := os.ReadFile(filepath.Join(current, "go.mod"))
		if readErr == nil && bytes.Contains(
			content,
			[]byte("module github.com/spice-framework/spice-agent-coding\n"),
		) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatal("locate spice-agent-coding module root")
		}
		current = parent
	}
}
