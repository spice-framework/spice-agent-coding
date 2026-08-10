package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agent"
	"github.com/spice-framework/spice-agent-coding/internal/terminalcommand"
	agenttui "github.com/spice-framework/spice-agent-tui"
	spiceconfig "github.com/spice-framework/spice/config"
)

func TestGeneratedRunnerCheckConstructsAndStopsWithoutStartingShell(t *testing.T) {
	t.Parallel()
	application := newFakeTerminalApplication()
	runner := testTerminalRunner(application)
	if err := runner.Run(t.Context(), capturedTerminalOptions(t, []string{"--check"})); err != nil {
		t.Fatal(err)
	}
	if application.starts != 0 || application.stops != 1 || application.shell.runs != 0 {
		t.Fatalf(
			"check lifecycle starts=%d stops=%d shell=%d",
			application.starts,
			application.stops,
			application.shell.runs,
		)
	}
}

func TestGeneratedRunnerStartsShellAndReversesApplication(t *testing.T) {
	t.Parallel()
	application := newFakeTerminalApplication()
	runner := testTerminalRunner(application)
	if err := runner.Run(t.Context(), capturedTerminalOptions(t, nil)); err != nil {
		t.Fatal(err)
	}
	if application.starts != 1 || application.stops != 1 || application.shell.runs != 1 {
		t.Fatalf(
			"managed lifecycle starts=%d stops=%d shell=%d",
			application.starts,
			application.stops,
			application.shell.runs,
		)
	}

	application = newFakeTerminalApplication()
	runner = testTerminalRunner(application)
	options := capturedTerminalOptions(t, []string{"attach", "--endpoint", `\\.\pipe\spice-agent`})
	if err := runner.Run(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if application.starts != 1 || application.stops != 1 || application.shell.runs != 1 {
		t.Fatal("explicit attach did not use the generated shell lifecycle")
	}
}

func TestGeneratedRunnerJoinsConstructionStartShellAndStopFailures(t *testing.T) {
	t.Parallel()
	constructFailure := errors.New("construct failed")
	runner := &generatedRunner{newApplication: func(
		context.Context,
		spicegen.ApplicationOptions,
	) (terminalApplication, error) {
		return nil, constructFailure
	}}
	if err := runner.Run(t.Context(), capturedTerminalOptions(t, []string{"--check"})); !errors.Is(err, constructFailure) {
		t.Fatalf("construction error = %v", err)
	}

	startFailure := errors.New("start failed")
	stopFailure := errors.New("stop failed")
	application := newFakeTerminalApplication()
	application.startErr = startFailure
	application.stopErr = stopFailure
	if err := testTerminalRunner(application).Run(t.Context(), capturedTerminalOptions(t, nil)); !errors.Is(err, startFailure) || !errors.Is(err, stopFailure) {
		t.Fatalf("start/stop error = %v", err)
	}

	shellFailure := errors.New("shell failed")
	application = newFakeTerminalApplication()
	application.shell.err = shellFailure
	application.stopErr = stopFailure
	if err := testTerminalRunner(application).Run(t.Context(), capturedTerminalOptions(t, nil)); !errors.Is(err, shellFailure) || !errors.Is(err, stopFailure) {
		t.Fatalf("shell/stop error = %v", err)
	}
}

func TestGeneratedRunnerMapsImmutableHighestPrecedenceInvocation(t *testing.T) {
	t.Parallel()
	base, err := spiceconfig.NewMapSource("base", map[string]string{"agent.terminal.mode": "wrong"})
	if err != nil {
		t.Fatal(err)
	}
	runner := &generatedRunner{options: spicegen.ApplicationOptions{Sources: []spiceconfig.Source{base}}}
	options := capturedTerminalOptions(t, []string{"attach", "--endpoint", "/tmp/spice-agent.sock"})
	configured, err := runner.optionsFor(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(configured.Sources) != 2 || len(runner.options.Sources) != 1 {
		t.Fatalf("sources configured=%d base=%d", len(configured.Sources), len(runner.options.Sources))
	}
	values, err := configured.Sources[1].Load(t.Context(), spiceconfig.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if values["agent.terminal.mode"] != "attach" ||
		values["agent.terminal.endpoint"] != "/tmp/spice-agent.sock" {
		t.Fatalf("invocation values = %v", values)
	}
}

func TestGeneratedRunnerRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()
	options := capturedTerminalOptions(t, []string{"--check"})
	if err := (&generatedRunner{}).Run(t.Context(), options); err == nil {
		t.Fatal("runner accepted nil factory")
	}
	if err := testTerminalRunner(newFakeTerminalApplication()).Run(nil, options); err == nil { //nolint:staticcheck // Boundary deliberately rejects nil context.
		t.Fatal("runner accepted nil context")
	}
	runner := &generatedRunner{newApplication: func(
		context.Context,
		spicegen.ApplicationOptions,
	) (terminalApplication, error) {
		return nil, nil //nolint:nilnil // Boundary verifies nil factory result.
	}}
	if err := runner.Run(t.Context(), options); err == nil {
		t.Fatal("runner accepted nil application")
	}
	if err := (&generatedRunner{}).stop(nil); err == nil {
		t.Fatal("stop accepted nil application")
	}
	application := newFakeTerminalApplication()
	application.timeout = 0
	if err := (&generatedRunner{}).stop(application); err == nil || application.stops != 0 {
		t.Fatalf("invalid timeout error=%v stops=%d", err, application.stops)
	}
}

func TestExecuteHelpAndGeneratedCheck(t *testing.T) {
	var stdout, stderr bytes.Buffer
	applicationCommand := command{input: bytes.NewReader(nil), stdout: &stdout, stderr: &stderr}
	if code := applicationCommand.execute(t.Context(), []string{"help"}); code != terminalcommand.ExitSuccess || stdout.Len() == 0 || stderr.Len() != 0 {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := applicationCommand.execute(t.Context(), []string{"--check"}); code != terminalcommand.ExitSuccess {
		t.Fatalf("check code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestExecuteRejectsUnavailableTerminalStreams(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	applicationCommand := command{stderr: &stderr}
	if code := applicationCommand.execute(t.Context(), nil); code != terminalcommand.ExitFailure ||
		!strings.Contains(stderr.String(), "terminal is unavailable") {
		t.Fatalf("unavailable terminal code=%d stderr=%q", code, stderr.String())
	}
}

func testTerminalRunner(application terminalApplication) *generatedRunner {
	return &generatedRunner{newApplication: func(
		context.Context,
		spicegen.ApplicationOptions,
	) (terminalApplication, error) {
		return application, nil
	}}
}

func capturedTerminalOptions(t *testing.T, arguments []string) terminalcommand.Options {
	t.Helper()
	var captured terminalcommand.Options
	code := (terminalcommand.Command{}).Execute(
		t.Context(),
		arguments,
		io.Discard,
		io.Discard,
		terminalcommand.RunnerFunc(func(_ context.Context, options terminalcommand.Options) error {
			captured = options
			return nil
		}),
	)
	if code != terminalcommand.ExitSuccess {
		t.Fatalf("capture terminal options code = %d", code)
	}
	return captured
}

type fakeShell struct {
	runs int
	err  error
}

func (shell *fakeShell) Run(context.Context) error {
	shell.runs++
	return shell.err
}

type fakeTerminalApplication struct {
	shell    *fakeShell
	timeout  time.Duration
	startErr error
	stopErr  error
	starts   int
	stops    int
}

func newFakeTerminalApplication() *fakeTerminalApplication {
	return &fakeTerminalApplication{shell: &fakeShell{}, timeout: time.Second}
}

func (application *fakeTerminalApplication) Start(context.Context) error {
	application.starts++
	return application.startErr
}

func (application *fakeTerminalApplication) Stop(ctx context.Context) error {
	application.stops++
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("stop context has no deadline")
	}
	return application.stopErr
}

func (application *fakeTerminalApplication) ShutdownTimeout() time.Duration {
	return application.timeout
}
func (application *fakeTerminalApplication) Shell() agenttui.Shell { return application.shell }
