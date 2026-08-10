package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent-coding/internal/daemoncommand"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
	spiceconfig "github.com/spice-framework/spice/config"
)

func TestGeneratedApplicationAcceptsDocumentedProcessEnvironment(t *testing.T) {
	const secret = "daemon-environment-test-secret"
	t.Setenv("OPENAI_API_KEY", secret)
	t.Setenv("OPENAI_MODEL", "daemon-environment-test-model")
	t.Setenv("SPICE_AGENT_WORKSPACE", t.TempDir())
	// Empty selects the protected current-user default. Arbitrary temporary
	// directories deliberately fail the authority's secure-ancestry checks.
	t.Setenv("SPICE_AGENT_RUN_AUTHORITY_DIRECTORY", "")
	environment, err := spiceconfig.OSEnvironment("SPICE_")
	if err != nil {
		t.Fatal(err)
	}
	application, err := spicegen.NewApplicationWithOptions(
		context.Background(),
		spicegen.ApplicationOptions{Sources: []spiceconfig.Source{environment}},
	)
	if err != nil {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("generated construction failure exposed the API key")
		}
		t.Fatalf("construct generated application from process environment: %v", err)
	}
	if err = (&generatedRunner{}).stop(generatedApplication{Application: application}); err != nil {
		t.Fatalf("stop generated application: %v", err)
	}
}

func TestGeneratedRunnerCheckConstructsAndCleansWithoutStarting(t *testing.T) {
	t.Parallel()
	application := newFakeApplication()
	runner := testGeneratedRunner(application)
	options := capturedOptions(t, []string{"--check"})

	if err := runner.Run(context.Background(), options); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if application.starts != 0 || application.stops != 1 {
		t.Fatalf("lifecycle starts=%d stops=%d, want 0/1", application.starts, application.stops)
	}
}

func TestGeneratedRunnerServesUntilCallerCancellationThenDrains(t *testing.T) {
	t.Parallel()
	application := newFakeApplication()
	runner := testGeneratedRunner(application)
	options := capturedOptions(t, []string{"serve"})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runner.Run(ctx, options) }()
	select {
	case <-application.started:
	case <-time.After(time.Second):
		t.Fatal("application did not start")
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if application.starts != 1 || application.stops != 1 {
		t.Fatalf("lifecycle starts=%d stops=%d, want 1/1", application.starts, application.stops)
	}
}

func TestGeneratedRunnerPropagatesTransportAndLifecycleFailures(t *testing.T) {
	t.Parallel()
	transportFailure := errors.New("transport failed")
	stopFailure := errors.New("stop failed")
	application := newFakeApplication()
	application.runtimeErr = transportFailure
	application.stopErr = stopFailure
	runner := testGeneratedRunner(application)
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(context.Background(), capturedOptions(t, []string{"serve"}))
	}()
	<-application.started
	close(application.runtimeDone)
	if err := <-result; !errors.Is(err, transportFailure) || !errors.Is(err, stopFailure) {
		t.Fatalf("Run() error = %v, want transport and stop failures", err)
	}
}

func TestGeneratedRunnerCleansAfterStartFailureAndRejectsInvalidState(t *testing.T) {
	t.Parallel()
	startFailure := errors.New("start failed")
	application := newFakeApplication()
	application.startErr = startFailure
	runner := testGeneratedRunner(application)
	if err := runner.Run(context.Background(), capturedOptions(t, []string{"serve"})); !errors.Is(err, startFailure) {
		t.Fatalf("Run() error = %v, want %v", err, startFailure)
	}
	if application.stops != 1 {
		t.Fatalf("cleanup calls = %d, want 1", application.stops)
	}

	if err := (&generatedRunner{}).Run(context.Background(), capturedOptions(t, []string{"serve"})); err == nil {
		t.Fatal("runner accepted a nil application factory")
	}
	var missingContext context.Context
	if err := testGeneratedRunner(newFakeApplication()).Run(missingContext, capturedOptions(t, []string{"serve"})); err == nil {
		t.Fatal("runner accepted a nil context")
	}
}

func TestGeneratedRunnerReportsFactoryAndUnexpectedTransportFailures(t *testing.T) {
	t.Parallel()
	factoryFailure := errors.New("factory failed")
	runner := &generatedRunner{newApplication: func(context.Context, spicegen.ApplicationOptions) (daemonApplication, error) {
		return nil, factoryFailure
	}}
	if err := runner.Run(context.Background(), capturedOptions(t, []string{"--check"})); !errors.Is(err, factoryFailure) {
		t.Fatalf("factory Run() error = %v, want %v", err, factoryFailure)
	}
	runner.newApplication = func(context.Context, spicegen.ApplicationOptions) (daemonApplication, error) {
		return nil, nil //nolint:nilnil // Deliberate invalid factory-result boundary.
	}
	if err := runner.Run(context.Background(), capturedOptions(t, []string{"--check"})); err == nil {
		t.Fatal("Run() accepted a nil application")
	}

	application := newFakeApplication()
	runner = testGeneratedRunner(application)
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(context.Background(), capturedOptions(t, []string{"serve"}))
	}()
	<-application.started
	close(application.runtimeDone)
	if err := <-result; err == nil {
		t.Fatal("Run() accepted an unexpected clean transport exit")
	}
}

func TestStopApplicationRejectsInvalidApplicationAndTimeout(t *testing.T) {
	t.Parallel()
	if err := (&generatedRunner{}).stop(nil); err == nil {
		t.Fatal("stopApplication() accepted nil")
	}
	application := newFakeApplication()
	application.shutdownTimeout = 0
	if err := (&generatedRunner{}).stop(application); err == nil || application.stops != 0 {
		t.Fatalf("stopApplication() error=%v stops=%d", err, application.stops)
	}
}

func TestGeneratedApplicationDelegatesRuntimeStatus(t *testing.T) {
	t.Parallel()
	application := generatedApplication{Application: &spicegen.Application{}}
	select {
	case <-application.RuntimeDone():
	default:
		t.Fatal("nil generated runtime did not report completion")
	}
	if err := application.RuntimeErr(); err == nil {
		t.Fatal("nil generated runtime did not report an error")
	}
}

func TestParentControlDrainsUntilEOFAndFailsClosedOnReadError(t *testing.T) {
	t.Parallel()
	for _, input := range []io.Reader{
		bytes.NewReader(nil),
		bytes.NewBufferString("data before EOF"),
		errorReader{},
	} {
		ctx, cancel := (command{input: input}).withParentControl(context.Background(), false)
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("control channel termination did not cancel daemon context")
		}
		cancel()
	}

	ctx, cancel := (command{input: bytes.NewReader(nil)}).withParentControl(context.Background(), true)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("terminal input unexpectedly controlled daemon lifetime")
	case <-time.After(20 * time.Millisecond):
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("control read failed") }

func TestExecuteHelpDoesNotExposeConfigurationOrConstructApplication(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	applicationCommand := command{stdout: &stdout, stderr: &stderr}
	if code := applicationCommand.execute(context.Background(), []string{"help"}); code != daemoncommand.ExitSuccess {
		t.Fatalf("execute() code = %d, stderr=%q", code, stderr.String())
	}
	if stdout.Len() == 0 || stderr.Len() != 0 {
		t.Fatalf("help output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestExecuteCheckConstructsAndCleansGeneratedGraph(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "command-check-secret")
	t.Setenv("OPENAI_MODEL", "command-check-model")
	t.Setenv("SPICE_AGENT_WORKSPACE", t.TempDir())
	t.Setenv("SPICE_AGENT_RUN_AUTHORITY_DIRECTORY", "")
	var stdout, stderr bytes.Buffer
	applicationCommand := command{stdout: &stdout, stderr: &stderr}
	if code := applicationCommand.execute(context.Background(), []string{"--check"}); code != daemoncommand.ExitSuccess {
		t.Fatalf("execute() code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "command-check-secret") || strings.Contains(stderr.String(), "command-check-secret") {
		t.Fatal("execute check exposed configuration secret")
	}
}

func TestIsTerminalRejectsNilAndRegularFiles(t *testing.T) {
	t.Parallel()
	if (command{}).isTerminal() {
		t.Fatal("isTerminal(nil) = true")
	}
	file, err := os.CreateTemp(t.TempDir(), "regular-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close regular file: %v", closeErr)
		}
	}()
	if (command{input: file}).isTerminal() {
		t.Fatal("isTerminal(regular file) = true")
	}
}

func TestGeneratedRunnerPassesApplicationOptionsToFactory(t *testing.T) {
	t.Parallel()
	source, err := spiceconfig.NewMapSource("test", map[string]string{"key": "value"})
	if err != nil {
		t.Fatal(err)
	}
	application := newFakeApplication()
	called := false
	runner := &generatedRunner{
		options: spicegen.ApplicationOptions{Sources: []spiceconfig.Source{source}},
		newApplication: func(_ context.Context, options spicegen.ApplicationOptions) (daemonApplication, error) {
			called = true
			if len(options.Sources) != 1 || options.Sources[0].Name() != "test" {
				t.Fatalf("factory options = %#v", options)
			}
			return application, nil
		},
	}
	if err = runner.Run(context.Background(), capturedOptions(t, []string{"--check"})); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !called {
		t.Fatal("application factory was not called")
	}
}

func testGeneratedRunner(application daemonApplication) *generatedRunner {
	return &generatedRunner{newApplication: func(context.Context, spicegen.ApplicationOptions) (daemonApplication, error) {
		return application, nil
	}}
}

func capturedOptions(t *testing.T, arguments []string) daemoncommand.Options {
	t.Helper()
	var captured daemoncommand.Options
	code := (daemoncommand.Command{}).Execute(
		context.Background(), arguments, io.Discard, io.Discard,
		daemoncommand.RunnerFunc(func(_ context.Context, options daemoncommand.Options) error {
			captured = options
			return nil
		}),
	)
	if code != daemoncommand.ExitSuccess {
		t.Fatalf("capture command options code = %d", code)
	}
	return captured
}

type fakeApplication struct {
	shutdownTimeout time.Duration
	started         chan struct{}
	runtimeDone     chan struct{}
	startErr        error
	stopErr         error
	runtimeErr      error
	starts          int
	stops           int
}

func newFakeApplication() *fakeApplication {
	return &fakeApplication{
		shutdownTimeout: time.Second,
		started:         make(chan struct{}),
		runtimeDone:     make(chan struct{}),
	}
}

func (application *fakeApplication) Start(context.Context) error {
	application.starts++
	close(application.started)
	return application.startErr
}

func (application *fakeApplication) Stop(ctx context.Context) error {
	application.stops++
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("stop context has no deadline")
	}
	return application.stopErr
}

func (application *fakeApplication) ShutdownTimeout() time.Duration {
	return application.shutdownTimeout
}

func (application *fakeApplication) RuntimeDone() <-chan struct{} { return application.runtimeDone }
func (application *fakeApplication) RuntimeErr() error            { return application.runtimeErr }
