package daemoncommand_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spice-framework/spice-agent-coding/internal/daemoncommand"
)

func TestExecuteAcceptedModesPreserveArgumentsAndContext(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		arguments []string
		mode      daemoncommand.Mode
		warning   bool
	}{
		{name: "serve", arguments: []string{"serve"}, mode: daemoncommand.ModeServe, warning: true},
		{name: "check", arguments: []string{"--check"}, mode: daemoncommand.ModeCheck},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			type contextKey struct{}
			ctx := context.WithValue(context.Background(), contextKey{}, test.name)
			arguments := append([]string(nil), test.arguments...)
			var calls int
			var stderr bytes.Buffer
			runner := daemoncommand.RunnerFunc(func(callContext context.Context, options daemoncommand.Options) error {
				calls++
				if callContext != ctx || callContext.Value(contextKey{}) != test.name {
					t.Fatal("runner did not receive the caller context")
				}
				if options.Mode() != test.mode {
					t.Fatalf("mode = %v, want %v", options.Mode(), test.mode)
				}
				arguments[0] = "mutated outside"
				first := options.Arguments()
				if strings.Join(first, " ") != strings.Join(test.arguments, " ") {
					t.Fatalf("arguments = %q, want %q", first, test.arguments)
				}
				first[0] = "mutated copy"
				if strings.Join(options.Arguments(), " ") != strings.Join(test.arguments, " ") {
					t.Fatal("options arguments are mutable through their getter")
				}
				return nil
			})

			if code := daemoncommand.Execute(ctx, arguments, io.Discard, &stderr, runner); code != daemoncommand.ExitSuccess {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
			}
			if calls != 1 {
				t.Fatalf("runner calls = %d, want 1", calls)
			}
			if got := strings.Contains(stderr.String(), "WARNING:"); got != test.warning {
				t.Fatalf("warning present = %v, want %v: %q", got, test.warning, stderr.String())
			}
		})
	}
}

func TestExecuteHelpDoesNotRequireRunner(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		code := daemoncommand.Execute(context.Background(), arguments, &stdout, &stderr, nil)
		if code != daemoncommand.ExitSuccess || !strings.Contains(stdout.String(), "spice-agentd serve") ||
			!strings.Contains(stdout.String(), "WARNING:") || stderr.Len() != 0 {
			t.Fatalf("help %q = code %d, stdout %q, stderr %q", arguments, code, stdout.String(), stderr.String())
		}
	}
}

func TestExecuteRejectsInvalidArgumentsWithoutReflection(t *testing.T) {
	t.Parallel()
	secret := "cli-secret-must-not-appear"
	for _, arguments := range [][]string{
		nil,
		{"serve", "extra"},
		{"Serve"},
		{secret},
		{"--check", secret},
	} {
		var stderr bytes.Buffer
		called := false
		code := daemoncommand.Execute(context.Background(), arguments, io.Discard, &stderr,
			daemoncommand.RunnerFunc(func(context.Context, daemoncommand.Options) error {
				called = true
				return nil
			}))
		if code != daemoncommand.ExitUsage || called {
			t.Fatalf("arguments %q = code %d, called %v", arguments, code, called)
		}
		if strings.Contains(stderr.String(), secret) || !strings.Contains(stderr.String(), "invalid arguments") {
			t.Fatalf("unsafe diagnostic for %q: %q", arguments, stderr.String())
		}
	}
}

func TestExecutePropagatesCancellationAndRedactsRunnerError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan int, 1)
	secret := "runner-secret-must-not-appear"
	var stderr bytes.Buffer
	go func() {
		done <- daemoncommand.Execute(ctx, []string{"serve"}, io.Discard, &stderr,
			daemoncommand.RunnerFunc(func(callContext context.Context, _ daemoncommand.Options) error {
				close(started)
				<-callContext.Done()
				return errors.New(secret + ": " + callContext.Err().Error())
			}))
	}()
	<-started
	cancel()
	if code := <-done; code != daemoncommand.ExitFailure {
		t.Fatalf("exit code = %d, want %d", code, daemoncommand.ExitFailure)
	}
	if strings.Contains(stderr.String(), secret) || !strings.Contains(stderr.String(), "operation failed") {
		t.Fatalf("unsafe runtime diagnostic: %q", stderr.String())
	}
}

func TestExecuteContainsRunnerPanicAndTypedNil(t *testing.T) {
	t.Parallel()
	secret := "panic-secret-must-not-appear"
	for _, test := range []struct {
		name   string
		runner daemoncommand.Runner
	}{
		{name: "typed nil", runner: daemoncommand.RunnerFunc(nil)},
		{name: "panic", runner: daemoncommand.RunnerFunc(func(context.Context, daemoncommand.Options) error {
			panic(secret)
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			code := daemoncommand.Execute(context.Background(), []string{"serve"}, io.Discard, &stderr, test.runner)
			if code != daemoncommand.ExitFailure || strings.Contains(stderr.String(), secret) ||
				!strings.Contains(stderr.String(), "operation failed") {
				t.Fatalf("contained runner = code %d, stderr %q", code, stderr.String())
			}
		})
	}
}

func TestExecuteRejectsUnavailableExecutionAndOutput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		nilCtx    bool
		nilRunner bool
		cancelled bool
	}{
		{name: "nil context", nilCtx: true},
		{name: "nil runner", nilRunner: true},
		{name: "cancelled context", cancelled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			if test.nilCtx {
				ctx = nil
			}
			if test.cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			var runner daemoncommand.Runner = daemoncommand.RunnerFunc(func(context.Context, daemoncommand.Options) error {
				t.Fatal("runner called")
				return nil
			})
			if test.nilRunner {
				runner = nil
			}
			if code := daemoncommand.Execute(ctx, []string{"--check"}, nil, io.Discard, runner); code != daemoncommand.ExitFailure {
				t.Fatalf("exit code = %d, want %d", code, daemoncommand.ExitFailure)
			}
		})
	}

	if code := daemoncommand.Execute(context.Background(), []string{"help"}, errorWriter{}, io.Discard, nil); code != daemoncommand.ExitFailure {
		t.Fatalf("help output failure code = %d", code)
	}
	if code := daemoncommand.Execute(context.Background(), []string{"invalid"}, io.Discard, errorWriter{}, nil); code != daemoncommand.ExitFailure {
		t.Fatalf("usage output failure code = %d", code)
	}
	if code := daemoncommand.Execute(context.Background(), []string{"help"}, shortWriter{}, io.Discard, nil); code != daemoncommand.ExitFailure {
		t.Fatalf("short help output code = %d", code)
	}
	runtimeCalled := false
	if code := daemoncommand.Execute(context.Background(), []string{"--check"}, io.Discard, errorWriter{},
		daemoncommand.RunnerFunc(func(context.Context, daemoncommand.Options) error {
			runtimeCalled = true
			return errors.New("runtime failed")
		})); code != daemoncommand.ExitFailure || !runtimeCalled {
		t.Fatalf("runtime output failure = code %d, called %v", code, runtimeCalled)
	}
	called := false
	if code := daemoncommand.Execute(context.Background(), []string{"serve"}, io.Discard, errorWriter{},
		daemoncommand.RunnerFunc(func(context.Context, daemoncommand.Options) error { called = true; return nil })); code != daemoncommand.ExitFailure || called {
		t.Fatalf("warning output failure = code %d, called %v", code, called)
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) {
	return len(value) - 1, nil
}
