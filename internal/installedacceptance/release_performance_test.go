//go:build spice_release_artifacts

package installedacceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Kodecable/crosspty"
	"github.com/spice-framework/spice-agent-tui/tuittest"
	"github.com/spice-framework/spice-agent/client"
	"github.com/spice-framework/spice-agent/client/localclient"
)

const (
	installedPerformanceSamples    = 5
	installedPerformanceHarnessSHA = "b33067c66885b4e287f7ac308dc66464c6473d99"
	installedPerformanceReleaseSHA = "96fefab2cfcd4ff849582e0d4d328ec8c782f16d"
)

var installedPerformancePlatforms = []string{"linux/amd64", "windows/amd64"}

var installedPerformanceMetrics = []string{
	"build-both-binaries",
	"daemon-plugin-ready",
	"generation-check",
	"ipc-initialize-health",
	"plugin-cancel-terminal",
	"plugin-drain-shutdown",
	"plugin-execute",
	"start-first-event",
	"tui-first-frame",
	"tui-reconnect",
	"tui-resize",
}

type installedPerformanceSample map[string]time.Duration

// TestCaptureStartFirstEventEvidence is a narrow baseline-capture seam used
// only when the definition of this metric changes. The release gate runs the
// same measurement as part of verifyInstalledPerformanceSamples.
func TestCaptureStartFirstEventEvidence(t *testing.T) {
	set := verifiedReleaseSet(t)
	installRoot, err := set.ExtractNative(
		filepath.Join(t.TempDir(), "start evidence installed bytes"), runtime.GOOS, runtime.GOARCH,
	)
	if err != nil {
		t.Fatal(err)
	}
	daemonBinary := filepath.Join(installRoot, executableName("spice-agentd"))
	root := repositoryRoot(t)
	plugin := buildOfflineTestBinary(t, root, "spice-agent-distribution-fixture", "./testdata/runtimeplugin/go")
	pluginDigest := fileSHA256(t, plugin)
	values := make([]int64, 0, installedPerformanceSamples)
	for index := range installedPerformanceSamples {
		t.Run(fmt.Sprintf("sample_%d", index+1), func(t *testing.T) {
			provider := newDecisiveReleaseProvider(t)
			store, environment := releaseProcessEnvironment(t)
			environment["OPENAI_BASE_URL"] = provider.server.URL + "/v1"
			environment["OPENAI_MODEL"] = "decisive-release-model"
			workspace := t.TempDir()
			if writeErr := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(decisiveWorkspaceInput), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			environment["SPICE_AGENT_WORKSPACE"] = workspace
			configureAcceptancePlugin(environment, plugin, pluginDigest)
			environment["SPICE_AGENT_RUNTIME_PLUGIN_STARTUP_TIMEOUT"] = "30s"
			daemon := startProcess(t, daemonBinary, []string{"serve"}, environment)
			t.Cleanup(func() { daemon.stop(t, false) })
			metadata := waitForEndpoint(t, store, daemon, nil, "")
			connector, connectorErr := localclient.New(metadata)
			if connectorErr != nil {
				t.Fatal(connectorErr)
			}
			session, initializeErr := connector.Initialize(
				t.Context(), decisiveInitializeRequest(t, metadata.Protocol(), nil),
			)
			if initializeErr != nil {
				t.Fatal(initializeErr)
			}
			definition := session.Connection().Catalog().Definitions()[0]
			started, invokedAt := decisiveStartAt(t, session, definition.Ref(), "decisive workflow")
			events, durations := timedDecisiveEvents(t, session, started.Run(), invokedAt, nil)
			assertDecisiveEvents(t, events, client.EventRunCompleted)
			values = append(values, durations.firstEvent.Microseconds())
			if closeErr := session.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if closeErr := connector.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			daemon.stop(t, true)
		})
	}
	slices.Sort(values)
	t.Logf("start-first-event %s/%s microseconds: %v", runtime.GOOS, runtime.GOARCH, values)
}

func verifyInstalledPerformanceSamples(t *testing.T) {
	set := verifiedReleaseSet(t)
	installRoot, err := set.ExtractNative(
		filepath.Join(t.TempDir(), "performance π installed bytes"), runtime.GOOS, runtime.GOARCH,
	)
	if err != nil {
		t.Fatalf("extract performance native release archive: %v", err)
	}
	daemonBinary := filepath.Join(installRoot, executableName("spice-agentd"))
	terminalBinary := filepath.Join(installRoot, executableName("spice-agent"))
	root := repositoryRoot(t)
	plugin := buildOfflineTestBinary(t, root, "spice-agent-distribution-fixture", "./testdata/runtimeplugin/go")
	pluginDigest := fileSHA256(t, plugin)

	samples := make([]installedPerformanceSample, 0, installedPerformanceSamples)
	for index := range installedPerformanceSamples {
		t.Run(fmt.Sprintf("sample_%d", index+1), func(t *testing.T) {
			samples = append(samples, measureInstalledRelease(
				t, root, daemonBinary, terminalBinary, plugin, pluginDigest, set,
			))
		})
	}
	encoded, err := json.MarshalIndent(installedPerformanceMicroseconds(samples), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("installed performance samples %s/%s:\n%s", runtime.GOOS, runtime.GOARCH, encoded)
	budget := readInstalledPerformanceBudget(t, root)
	if err = enforceInstalledPerformanceBudget(
		budget,
		runtime.GOOS+"/"+runtime.GOARCH,
		installedPerformanceMicroseconds(samples),
	); err != nil {
		t.Fatal(err)
	}
}

func measureInstalledRelease(
	t *testing.T,
	root, daemonBinary, terminalBinary, plugin, pluginDigest string,
	set interface{ Version() string },
) installedPerformanceSample {
	t.Helper()
	provider := newDecisiveReleaseProvider(t)
	store, environment := releaseProcessEnvironment(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(decisiveWorkspaceInput), 0o600); err != nil {
		t.Fatal(err)
	}
	environment["OPENAI_BASE_URL"] = provider.server.URL + "/v1"
	environment["OPENAI_MODEL"] = "decisive-release-model"
	environment["SPICE_AGENT_WORKSPACE"] = workspace
	configureAcceptancePlugin(environment, plugin, pluginDigest)
	environment["SPICE_AGENT_RUNTIME_PLUGIN_STARTUP_TIMEOUT"] = "30s"
	result := make(installedPerformanceSample)

	startedAt := time.Now()
	daemonOne := startProcess(t, daemonBinary, []string{"serve"}, environment)
	t.Cleanup(func() { daemonOne.stop(t, false) })
	metadataOne := waitForEndpoint(t, store, daemonOne, nil, "")
	result["daemon-plugin-ready"] = time.Since(startedAt)
	if metadataOne.Server().Version() != strings.TrimPrefix(set.Version(), "v") {
		t.Fatalf("performance daemon advertised version %q", metadataOne.Server().Version())
	}

	connector, err := localclient.New(metadataOne)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := connector.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	startedAt = time.Now()
	session, err := connector.Initialize(
		t.Context(), decisiveInitializeRequest(t, metadataOne.Protocol(), nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	health, err := session.Health(t.Context())
	if err != nil || health.State() != client.HealthReady {
		t.Fatalf("performance health = %s, error %v", health.State(), err)
	}
	result["ipc-initialize-health"] = time.Since(startedAt)
	definition := session.Connection().Catalog().Definitions()[0]
	started, startInvokedAt := decisiveStartAt(t, session, definition.Ref(), "decisive workflow")
	events, eventDurations := timedDecisiveEvents(t, session, started.Run(), startInvokedAt, nil)
	assertDecisiveEvents(t, events, client.EventRunCompleted)
	result["start-first-event"] = eventDurations.firstEvent
	result["plugin-execute"] = eventDurations.plugin

	claim, err := client.NewReconnectClaim(
		session.Connection().ClientID(), session.Connection().OwnershipEpoch(),
	)
	if err != nil {
		t.Fatal(err)
	}
	reconnected, err := connector.Initialize(
		t.Context(), decisiveInitializeRequest(t, metadataOne.Protocol(), &claim),
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancelInvokedAt := decisiveStartAt(t, reconnected, definition.Ref(), "cancel plugin")
	events, eventDurations = timedDecisiveEvents(t, reconnected, cancelled.Run(), cancelInvokedAt, func() {
		operation, operationErr := client.NewOperationID("performance-release-cancel")
		if operationErr != nil {
			t.Fatal(operationErr)
		}
		request, requestErr := client.NewCancelRequest(cancelled.Run(), operation, "performance proof")
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		if _, requestErr = reconnected.Cancel(t.Context(), request); requestErr != nil {
			t.Fatal(requestErr)
		}
	})
	assertDecisiveEvents(t, events, client.EventRunCancelled)
	result["plugin-cancel-terminal"] = eventDurations.cancel
	provider.assertCancellation(t)
	if err = reconnected.Close(); err != nil {
		t.Fatal(err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	if err = connector.Close(); err != nil {
		t.Fatal(err)
	}

	nativeEnvironment := releasedNativeTerminalEnvironment(t, environment)
	startedAt = time.Now()
	terminal := startReleasedNativeTerminal(
		t, terminalBinary, []string{"attach", "--endpoint", metadataOne.Address()}, nativeEnvironment,
		crosspty.KillModeKillGroupOnSubProcessExit,
	)
	waitForReleasedNativeScreen(t, terminal, "performance-first-frame", func(screen tuittest.Screen) bool {
		return screen.AlternateScreen() && screen.Contains("Spice Agent")
	})
	result["tui-first-frame"] = time.Since(startedAt)
	startedAt = time.Now()
	if err = terminal.resize(nativeTerminalResizedWidth, nativeTerminalResizedHeight); err != nil {
		t.Fatal(err)
	}
	waitForReleasedNativeScreen(t, terminal, "performance-resize", func(screen tuittest.Screen) bool {
		return screen.Width() == nativeTerminalResizedWidth && screen.Height() == nativeTerminalResizedHeight
	})
	result["tui-resize"] = time.Since(startedAt)

	startedAt = time.Now()
	daemonOne.stop(t, true)
	result["plugin-drain-shutdown"] = time.Since(startedAt)
	startedAt = time.Now()
	daemonTwo := startProcess(t, daemonBinary, []string{"serve"}, environment)
	t.Cleanup(func() { daemonTwo.stop(t, false) })
	waitForEndpoint(t, store, daemonTwo, &metadataOne, "")
	waitForReleasedNativeScreen(t, terminal, "performance-reconnect", func(screen tuittest.Screen) bool {
		return screen.Contains("daemon connection restored with a fresh session")
	})
	result["tui-reconnect"] = time.Since(startedAt)
	quitReleasedNativeTerminal(t, terminal)
	daemonTwo.stop(t, true)
	assertEndpointAbsent(t, store, daemonTwo)

	result["generation-check"] = measureCommand(t, root,
		"go", "tool", "github.com/spice-framework/toolchain/cmd/spice", "generate", "--check",
		"--target", "ArchitectureProof", ".", "./internal/architectureproof",
	)
	buildRoot := t.TempDir()
	startedAt = time.Now()
	measureCommand(t, root, "go", "build", "-trimpath", "-o", filepath.Join(buildRoot, executableName("spice-agentd")), "./cmd/spice-agentd")
	measureCommand(t, root, "go", "build", "-trimpath", "-o", filepath.Join(buildRoot, executableName("spice-agent")), "./cmd/spice-agent")
	result["build-both-binaries"] = time.Since(startedAt)
	return result
}

type timedEventDurations struct {
	firstEvent time.Duration
	plugin     time.Duration
	cancel     time.Duration
}

func timedDecisiveEvents(
	t *testing.T,
	session client.Session,
	run client.RunRef,
	operationInvokedAt time.Time,
	cancelPlugin func(),
) ([]client.Event, timedEventDurations) {
	t.Helper()
	cursor, err := client.NewCursor(run, 0)
	if err != nil {
		t.Fatal(err)
	}
	options, err := client.NewEventStreamOptions(64, true, session.Connection().Limits())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := session.Events(t.Context(), cursor, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	var pluginStarted, cancelStarted time.Time
	var durations timedEventDurations
	var events []client.Event
	ctx, cancel := context.WithTimeout(t.Context(), observationTimeout)
	defer cancel()
	for {
		frame, nextErr := stream.Next(ctx)
		if nextErr != nil {
			t.Fatalf("read timed release events: %v", nextErr)
		}
		current, ok := frame.Event()
		if !ok {
			continue
		}
		if len(events) == 0 {
			durations.firstEvent = time.Since(operationInvokedAt)
		}
		events = append(events, current)
		if _, name, started := current.Detail().ToolStart(); started && name == "fixture.echo" {
			pluginStarted = time.Now()
		}
		if terminal, terminalOK := current.Detail().ToolTerminal(); terminalOK &&
			terminal.Name() == "fixture.echo" && !pluginStarted.IsZero() {
			durations.plugin = time.Since(pluginStarted)
		}
		if _, message, progress := current.Detail().ToolProgress(); progress &&
			message == "block ready" && cancelPlugin != nil && cancelStarted.IsZero() {
			cancelStarted = time.Now()
			cancelPlugin()
		}
		switch current.Kind() {
		case client.EventRunCompleted, client.EventRunCancelled, client.EventRunFailed:
			if !cancelStarted.IsZero() {
				durations.cancel = time.Since(cancelStarted)
			}
			return events, durations
		}
	}
}

func measureCommand(t *testing.T, root, executable string, arguments ...string) time.Duration {
	t.Helper()
	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if executable == "go" {
		executable = installedGoExecutable()
	}
	command := exec.CommandContext(ctx, executable, arguments...) // #nosec G204 -- exact repository-owned command and arguments.
	command.Dir = root
	command.Env = mergedEnvironment(map[string]string{
		"GOWORK": "off", "GOFLAGS": "-mod=vendor", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local",
	})
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("measure %s %s: %v\n%s", filepath.Base(executable), strings.Join(arguments, " "), err, output)
	}
	return time.Since(startedAt)
}

func installedPerformanceMicroseconds(samples []installedPerformanceSample) map[string][]int64 {
	result := make(map[string][]int64)
	for _, sample := range samples {
		for name, duration := range sample {
			result[name] = append(result[name], duration.Microseconds())
		}
	}
	for name := range result {
		slices.Sort(result[name])
	}
	return result
}

func medianMicroseconds(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	ordered := slices.Clone(values)
	slices.Sort(ordered)
	return ordered[len(ordered)/2]
}

type installedPerformanceBudget struct {
	Schema                              int                                `json:"schema"`
	HarnessCommit                       string                             `json:"harness_commit"`
	ReleaseCommit                       string                             `json:"release_commit"`
	Unit                                string                             `json:"unit"`
	Samples                             int                                `json:"samples"`
	Aggregation                         string                             `json:"aggregation"`
	TimeMaterialRegressionPercent       int                                `json:"time_material_regression_percent"`
	AllocationMaterialRegressionPercent int                                `json:"allocation_material_regression_percent"`
	AllocationScope                     string                             `json:"allocation_scope"`
	Platforms                           map[string]installedPlatformBudget `json:"platforms"`
}

type installedPlatformBudget struct {
	Evidence string                           `json:"evidence"`
	Metrics  map[string]installedMetricBudget `json:"metrics"`
}

type installedMetricBudget struct {
	Observed []int64 `json:"observed"`
	Median   int64   `json:"median"`
	Ceiling  int64   `json:"ceiling"`
}

func readInstalledPerformanceBudget(t *testing.T, root string) installedPerformanceBudget {
	t.Helper()
	path := filepath.Join(root, "benchmarks", "installed-performance.json")
	content, err := os.ReadFile(path) // #nosec G304 -- fixed repository contract path.
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var result installedPerformanceBudget
	if err = decoder.Decode(&result); err != nil {
		t.Fatalf("decode installed performance budget: %v", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		t.Fatal("installed performance budget has trailing JSON values")
	}
	return result
}

func enforceInstalledPerformanceBudget(
	budget installedPerformanceBudget,
	platform string,
	measured map[string][]int64,
) error {
	if err := validateInstalledPerformanceBudget(budget); err != nil {
		return err
	}
	if !slices.Contains(installedPerformancePlatforms, platform) {
		return fmt.Errorf("installed performance platform %q is unsupported", platform)
	}
	if !exactStringMapKeys(measured, installedPerformanceMetrics) {
		return errors.New("installed performance measured metric membership is invalid")
	}
	selected := budget.Platforms[platform]
	for _, name := range installedPerformanceMetrics {
		values := measured[name]
		if len(values) != installedPerformanceSamples || !slices.IsSorted(values) {
			return fmt.Errorf("installed performance metric %q must contain five ordered samples", name)
		}
		definition := selected.Metrics[name]
		median := medianMicroseconds(values)
		materialLimit := definition.Median + definition.Median*int64(budget.TimeMaterialRegressionPercent)/100
		if median <= 0 || median > materialLimit {
			return fmt.Errorf(
				"installed performance metric %q median %d us exceeds recorded median %d us plus %d%% (%d us)",
				name, median, definition.Median, budget.TimeMaterialRegressionPercent, materialLimit,
			)
		}
		if median > definition.Ceiling {
			return fmt.Errorf(
				"installed performance metric %q median %d us exceeds ceiling %d us",
				name, median, definition.Ceiling,
			)
		}
	}
	return nil
}

func validateInstalledPerformanceBudget(budget installedPerformanceBudget) error {
	if budget.Schema != 1 || budget.Unit != "microseconds" ||
		budget.HarnessCommit != installedPerformanceHarnessSHA ||
		budget.ReleaseCommit != installedPerformanceReleaseSHA ||
		budget.Samples != installedPerformanceSamples || budget.Aggregation != "median" ||
		budget.TimeMaterialRegressionPercent != 20 ||
		budget.AllocationMaterialRegressionPercent != 10 ||
		budget.AllocationScope != "not applicable to cross-process installed metrics" {
		return errors.New("installed performance budget header is invalid")
	}
	if !exactStringMapKeys(budget.Platforms, installedPerformancePlatforms) {
		return errors.New("installed performance platform membership is invalid")
	}
	for _, platform := range installedPerformancePlatforms {
		selected := budget.Platforms[platform]
		if strings.TrimSpace(selected.Evidence) == "" ||
			!exactStringMapKeys(selected.Metrics, installedPerformanceMetrics) {
			return fmt.Errorf("installed performance evidence for %s is invalid", platform)
		}
		for _, name := range installedPerformanceMetrics {
			definition := selected.Metrics[name]
			if len(definition.Observed) != installedPerformanceSamples ||
				!slices.IsSorted(definition.Observed) ||
				definition.Median != medianMicroseconds(definition.Observed) ||
				definition.Ceiling <= definition.Observed[len(definition.Observed)-1] ||
				definition.Ceiling >= (2*time.Minute).Microseconds() {
				return fmt.Errorf("installed performance metric %s/%s evidence is invalid", platform, name)
			}
		}
	}
	return nil
}

func exactStringMapKeys[T any](values map[string]T, expected []string) bool {
	if len(values) != len(expected) {
		return false
	}
	for _, name := range expected {
		if _, found := values[name]; !found {
			return false
		}
	}
	return true
}

func TestMedianMicrosecondsIsDeterministic(t *testing.T) {
	t.Parallel()
	input := []int64{50, 10, 30, 20, 40}
	if got := medianMicroseconds(input); got != 30 {
		t.Fatalf("median = %d", got)
	}
	if !slices.Equal(input, []int64{50, 10, 30, 20, 40}) {
		t.Fatal("median mutated its input")
	}
	if got := medianMicroseconds(nil); got != 0 {
		t.Fatalf("empty median = %d", got)
	}
}

func TestInstalledPerformanceBudgetRejectsDrift(t *testing.T) {
	t.Parallel()
	valid := validInstalledPerformanceBudgetForTest()
	measured := validInstalledPerformanceMeasurementsForTest(3)
	if err := enforceInstalledPerformanceBudget(
		valid, "windows/amd64", measured,
	); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*installedPerformanceBudget){
		func(value *installedPerformanceBudget) { value.Schema++ },
		func(value *installedPerformanceBudget) { value.TimeMaterialRegressionPercent++ },
		func(value *installedPerformanceBudget) { value.Platforms = nil },
		func(value *installedPerformanceBudget) {
			entry := value.Platforms["windows/amd64"]
			metric := entry.Metrics[installedPerformanceMetrics[0]]
			metric.Median++
			entry.Metrics[installedPerformanceMetrics[0]] = metric
		},
		func(value *installedPerformanceBudget) {
			entry := value.Platforms["windows/amd64"]
			metric := entry.Metrics[installedPerformanceMetrics[0]]
			metric.Ceiling = 5
			entry.Metrics[installedPerformanceMetrics[0]] = metric
		},
	} {
		candidate := valid
		candidate.Platforms = cloneInstalledPlatforms(valid.Platforms)
		mutate(&candidate)
		if err := enforceInstalledPerformanceBudget(
			candidate, "windows/amd64", measured,
		); err == nil {
			t.Fatal("mutated installed performance budget succeeded")
		}
	}
	materialRegression := validInstalledPerformanceMeasurementsForTest(4)
	if err := enforceInstalledPerformanceBudget(
		valid, "windows/amd64", materialRegression,
	); err == nil {
		t.Fatal("installed performance material regression succeeded below the absolute ceiling")
	}
}

func validInstalledPerformanceBudgetForTest() installedPerformanceBudget {
	result := installedPerformanceBudget{
		Schema: 1, HarnessCommit: installedPerformanceHarnessSHA,
		ReleaseCommit: installedPerformanceReleaseSHA,
		Unit:          "microseconds", Samples: installedPerformanceSamples, Aggregation: "median",
		TimeMaterialRegressionPercent: 20, AllocationMaterialRegressionPercent: 10,
		AllocationScope: "not applicable to cross-process installed metrics",
		Platforms:       make(map[string]installedPlatformBudget),
	}
	for _, platform := range installedPerformancePlatforms {
		metrics := make(map[string]installedMetricBudget)
		for _, name := range installedPerformanceMetrics {
			metrics[name] = installedMetricBudget{
				Observed: []int64{1, 2, 3, 4, 5}, Median: 3, Ceiling: 10,
			}
		}
		result.Platforms[platform] = installedPlatformBudget{Evidence: "measured", Metrics: metrics}
	}
	return result
}

func validInstalledPerformanceMeasurementsForTest(median int64) map[string][]int64 {
	result := make(map[string][]int64)
	for _, name := range installedPerformanceMetrics {
		result[name] = []int64{median - 2, median - 1, median, median + 1, median + 2}
	}
	return result
}

func cloneInstalledPlatforms(
	values map[string]installedPlatformBudget,
) map[string]installedPlatformBudget {
	result := make(map[string]installedPlatformBudget, len(values))
	for platform, current := range values {
		metrics := make(map[string]installedMetricBudget, len(current.Metrics))
		for name, metric := range current.Metrics {
			metric.Observed = slices.Clone(metric.Observed)
			metrics[name] = metric
		}
		current.Metrics = metrics
		result[platform] = current
	}
	return result
}
