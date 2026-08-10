package runtimepluginfixture_test

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	distributiondaemon "github.com/spice-framework/spice-agent-coding/internal/daemon"
	"github.com/spice-framework/spice-agent-coding/internal/processplatform"
	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
	"github.com/spice-framework/spice-agent/plugin/host/localendpoint"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
)

func TestFixtureCancellationReleasesSingleConcurrencySlotAndProcess(t *testing.T) {
	root := repositoryRoot(t)
	executablePath, digest := buildFixture(t, root)
	registrar := &cancellationRegistrar{}
	launcher, err := processplatform.NewLauncher(registrar)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := stage.NewDispatcher(map[string]tool.Tool{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := distributiondaemon.NewRuntimePluginPlan(distributiondaemon.RuntimePluginProperties{
		Required: true, ID: "distribution-cancellation-fixture", Path: executablePath,
		SHA256: digest, ManifestName: fixtureManifest, ManifestVersion: fixtureVersion,
		StartupTimeout: fixtureStartupTimeout, CallTimeout: 5 * time.Second,
		DrainTimeout: 5 * time.Second, ShutdownTimeout: 5 * time.Second,
		ContainmentTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("construct cancellation fixture plan: %v", err)
	}
	restart, err := distributiondaemon.NewRuntimePluginRestartPolicy(plan)
	if err != nil {
		t.Fatalf("construct cancellation fixture restart policy: %v", err)
	}
	host, err := pluginhost.NewHost(pluginhost.HostConfig{
		HostIdentity: &pluginv1.BuildIdentity{
			Component: "spice-agentd-cancellation-fixture-host", Version: "v1",
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
			t.Errorf("close cancellation fixture host during cleanup: %v", closeErr)
		}
	})

	activation, err := distributiondaemon.NewRuntimePluginActivation(plan, host)
	if err != nil {
		t.Fatal(err)
	}
	activationContext, cancelActivation := context.WithTimeout(t.Context(), fixtureActivationTimeout)
	err = activation.Start(activationContext)
	cancelActivation()
	if err != nil {
		t.Fatalf("activate cancellation fixture: %v", err)
	}
	if got := registrar.registeredPIDs(); len(got) != 1 || got[0] <= 0 {
		t.Fatalf("registered cancellation fixture processes = %v", got)
	}

	lease, err := host.LeaseCurrent(t.Context())
	if err != nil {
		t.Fatalf("lease cancellation fixture generation: %v", err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			if releaseErr := lease.Release(); releaseErr != nil {
				t.Errorf("release cancellation fixture lease during cleanup: %v", releaseErr)
			}
		}
	})
	definitions := lease.Definitions()
	if len(definitions) != 2 || definitions[0].Name() != fixtureBlock ||
		definitions[1].Name() != fixtureTool {
		t.Fatalf("cancellation fixture definitions = %#v", definitions)
	}

	blockCall, err := tool.NewCall("distribution-fixture-block-call", fixtureBlock, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	reporter := newCancellationReporter()
	callContext, cancelCall := context.WithCancel(t.Context())
	defer cancelCall()
	type dispatchOutcome struct {
		result tool.Result
		err    error
	}
	dispatchDone := make(chan dispatchOutcome, 1)
	go func() {
		result, dispatchErr := lease.Dispatcher().Dispatch(callContext, blockCall, reporter)
		dispatchDone <- dispatchOutcome{result: result, err: dispatchErr}
	}()

	readyContext, cancelReady := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelReady()
	select {
	case <-reporter.ready:
	case <-readyContext.Done():
		t.Fatalf("wait for blocking fixture readiness: %v", readyContext.Err())
	}
	cancelCall()

	terminalContext, cancelTerminal := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelTerminal()
	var outcome dispatchOutcome
	select {
	case outcome = <-dispatchDone:
	case <-terminalContext.Done():
		t.Fatalf("wait for canceled fixture terminal outcome: %v", terminalContext.Err())
	}
	if !outcome.result.IsZero() || !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("canceled fixture outcome = %#v, error = %v", outcome.result, outcome.err)
	}
	var failure *tool.ExecutionError
	if !errors.As(outcome.err, &failure) || failure.CallID() != blockCall.ID() ||
		failure.State() != tool.ExecutionDefinitive || failure.RetryDisposition() != tool.RetryNever {
		t.Fatalf("canceled fixture terminal failure = %#v", failure)
	}
	if got := reporter.messages(); len(got) != 1 || got[0] != "block ready" {
		t.Fatalf("blocking fixture progress = %#v", got)
	}

	echoCall, err := tool.NewCall(
		"distribution-fixture-after-cancel-call",
		fixtureTool,
		[]byte(`{"value":"after-cancel"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	echoContext, cancelEcho := context.WithTimeout(t.Context(), 5*time.Second)
	echoReporter := &recordingReporter{}
	echoResult, err := lease.Dispatcher().Dispatch(echoContext, echoCall, echoReporter)
	cancelEcho()
	if err != nil {
		t.Fatalf("dispatch echo after cancellation released the single slot: %v", err)
	}
	if string(echoResult.Content()) != `{"value":"after-cancel"}` {
		t.Fatalf("echo after cancellation result = %s", echoResult.Content())
	}
	if got := echoReporter.messages(); len(got) != 1 || got[0] != "echo accepted" {
		t.Fatalf("echo after cancellation progress = %#v", got)
	}

	if err = lease.Release(); err != nil {
		t.Fatalf("release cancellation fixture generation: %v", err)
	}
	released = true
	closeContext, cancelClose := context.WithTimeout(context.Background(), 10*time.Second)
	err = host.Close(closeContext)
	cancelClose()
	if err != nil {
		t.Fatalf("bounded cancellation fixture drain and shutdown: %v", err)
	}
	closed = true
	health := host.Health()
	if err = health.Validate(); err != nil || health.State() != pluginhost.HealthStateStopped ||
		health.ActiveLeases() != 0 || health.RetainedGenerations() != 0 {
		t.Fatalf("closed cancellation fixture host health = %s, validation = %v", health, err)
	}
	for _, pid := range registrar.registeredPIDs() {
		assertFixtureProcessExited(t, pid)
	}
}

type cancellationRegistrar struct {
	mu   sync.Mutex
	pids []int
}

func (registrar *cancellationRegistrar) Register(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return errors.New("cancellation fixture process registration is invalid")
	}
	registrar.mu.Lock()
	defer registrar.mu.Unlock()
	registrar.pids = append(registrar.pids, process.Pid)
	return nil
}

func (registrar *cancellationRegistrar) registeredPIDs() []int {
	registrar.mu.Lock()
	defer registrar.mu.Unlock()
	return append([]int(nil), registrar.pids...)
}

type cancellationReporter struct {
	once     sync.Once
	ready    chan struct{}
	mu       sync.Mutex
	progress []string
}

func newCancellationReporter() *cancellationReporter {
	return &cancellationReporter{ready: make(chan struct{})}
}

func (reporter *cancellationReporter) Report(ctx context.Context, progress tool.Progress) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reporter.mu.Lock()
	reporter.progress = append(reporter.progress, progress.Message())
	reporter.mu.Unlock()
	if progress.Message() == "block ready" {
		reporter.once.Do(func() { close(reporter.ready) })
	}
	return nil
}

func (reporter *cancellationReporter) messages() []string {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	return append([]string(nil), reporter.progress...)
}
