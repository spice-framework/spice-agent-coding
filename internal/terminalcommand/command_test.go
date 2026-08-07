package terminalcommand_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spice-framework/spice-agent-coding/internal/terminalcommand"
)

func TestExecuteAcceptedModesPreserveArgumentsEndpointAndContext(t *testing.T) {
	t.Parallel()
	endpoint := `\\.\pipe\Spice Agent Ω`
	for _, test := range []struct {
		name      string
		arguments []string
		mode      terminalcommand.Mode
		endpoint  string
		warning   bool
	}{
		{name: "managed", mode: terminalcommand.ModeManaged, warning: true},
		{name: "attach", arguments: []string{"attach", "--endpoint", endpoint}, mode: terminalcommand.ModeAttach, endpoint: endpoint, warning: true},
		{name: "check", arguments: []string{"--check"}, mode: terminalcommand.ModeCheck},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			type contextKey struct{}
			ctx := context.WithValue(context.Background(), contextKey{}, test.name)
			arguments := append([]string(nil), test.arguments...)
			var stderr bytes.Buffer
			calls := 0
			runner := terminalcommand.RunnerFunc(func(callContext context.Context, options terminalcommand.Options) error {
				calls++
				if callContext != ctx || callContext.Value(contextKey{}) != test.name {
					t.Fatal("runner did not receive caller context")
				}
				if options.Mode() != test.mode || options.Endpoint() != test.endpoint {
					t.Fatalf("options = mode %v endpoint %q", options.Mode(), options.Endpoint())
				}
				if len(arguments) > 0 {
					arguments[0] = "mutated outside"
				}
				first := options.Arguments()
				if strings.Join(first, "\x00") != strings.Join(test.arguments, "\x00") {
					t.Fatalf("arguments = %q, want %q", first, test.arguments)
				}
				if len(first) > 0 {
					first[0] = "mutated copy"
				}
				if strings.Join(options.Arguments(), "\x00") != strings.Join(test.arguments, "\x00") {
					t.Fatal("options arguments are mutable through their getter")
				}
				return nil
			})
			if code := terminalcommand.Execute(ctx, arguments, io.Discard, &stderr, runner); code != terminalcommand.ExitSuccess {
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

func TestExecuteAcceptsOpaqueLocalEndpointForms(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{
		"/tmp/spice agent.sock",
		"@spice-agent-1000",
		"spice-agent-user-scope",
		strings.Repeat("x", 4096),
		`C:\Users\Person\App Data\spice.sock`,
		`\\.\pipe\spice-agent`,
	} {
		captured := ""
		code := terminalcommand.Execute(context.Background(), []string{"attach", "--endpoint", endpoint}, io.Discard, io.Discard,
			terminalcommand.RunnerFunc(func(_ context.Context, options terminalcommand.Options) error {
				captured = options.Endpoint()
				return nil
			}))
		if code != terminalcommand.ExitSuccess || captured != endpoint {
			t.Fatalf("endpoint %q = code %d, captured %q", endpoint, code, captured)
		}
	}
}

func TestExecuteRejectsRemoteLookingAndMalformedEndpointsWithoutReflection(t *testing.T) {
	t.Parallel()
	secret := "endpoint-secret-must-not-appear"
	endpoints := []string{
		"",
		"https://" + secret + "@example.com",
		"tcp:example.com:1234",
		"grpc:example.com",
		"dns:example.com",
		"example.com:443",
		"localhost:9000",
		":9000",
		"example.com:",
		"[::1]:9000",
		"//server/share/socket",
		`\\server\pipe\` + secret,
		"bad\nendpoint",
		"bad\tendpoint",
		"bad\u0085endpoint",
		" /tmp/spice.sock",
		"/tmp/spice.sock ",
		string([]byte{0xff, 0xfe}),
		strings.Repeat("x", 4097),
	}
	for _, endpoint := range endpoints {
		var stderr bytes.Buffer
		called := false
		code := terminalcommand.Execute(context.Background(), []string{"attach", "--endpoint", endpoint}, io.Discard, &stderr,
			terminalcommand.RunnerFunc(func(context.Context, terminalcommand.Options) error { called = true; return nil }))
		if code != terminalcommand.ExitUsage || called {
			t.Fatalf("endpoint %q = code %d, called %v", endpoint, code, called)
		}
		if strings.Contains(stderr.String(), secret) || strings.Contains(stderr.String(), endpoint) && endpoint != "" {
			t.Fatalf("endpoint reflected in diagnostic: %q", stderr.String())
		}
	}
}

func TestExecuteRejectsInvalidGrammar(t *testing.T) {
	t.Parallel()
	secret := "argument-secret-must-not-appear"
	for _, arguments := range [][]string{
		{"attach"},
		{"attach", "--endpoint"},
		{"attach", "--endpoint=/tmp/spice.sock"},
		{"attach", "--endpoint", "/tmp/spice.sock", "extra"},
		{"managed"},
		{secret},
		{"--check", secret},
	} {
		var stderr bytes.Buffer
		code := terminalcommand.Execute(context.Background(), arguments, io.Discard, &stderr,
			terminalcommand.RunnerFunc(func(context.Context, terminalcommand.Options) error {
				t.Fatal("runner called for invalid grammar")
				return nil
			}))
		if code != terminalcommand.ExitUsage || strings.Contains(stderr.String(), secret) {
			t.Fatalf("arguments %q = code %d, stderr %q", arguments, code, stderr.String())
		}
	}
}

func TestExecuteHelpDoesNotRequireRunner(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		code := terminalcommand.Execute(context.Background(), arguments, &stdout, &stderr, nil)
		if code != terminalcommand.ExitSuccess || !strings.Contains(stdout.String(), "attach --endpoint") ||
			!strings.Contains(stdout.String(), "WARNING:") || stderr.Len() != 0 {
			t.Fatalf("help %q = code %d, stdout %q, stderr %q", arguments, code, stdout.String(), stderr.String())
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
		done <- terminalcommand.Execute(ctx, nil, io.Discard, &stderr,
			terminalcommand.RunnerFunc(func(callContext context.Context, _ terminalcommand.Options) error {
				close(started)
				<-callContext.Done()
				return errors.New(secret + ": " + callContext.Err().Error())
			}))
	}()
	<-started
	cancel()
	if code := <-done; code != terminalcommand.ExitFailure {
		t.Fatalf("exit code = %d, want %d", code, terminalcommand.ExitFailure)
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
		runner terminalcommand.Runner
	}{
		{name: "typed nil", runner: terminalcommand.RunnerFunc(nil)},
		{name: "panic", runner: terminalcommand.RunnerFunc(func(context.Context, terminalcommand.Options) error {
			panic(secret)
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			code := terminalcommand.Execute(context.Background(), nil, io.Discard, &stderr, test.runner)
			if code != terminalcommand.ExitFailure || strings.Contains(stderr.String(), secret) ||
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
			var runner terminalcommand.Runner = terminalcommand.RunnerFunc(func(context.Context, terminalcommand.Options) error {
				t.Fatal("runner called")
				return nil
			})
			if test.nilRunner {
				runner = nil
			}
			if code := terminalcommand.Execute(ctx, []string{"--check"}, nil, io.Discard, runner); code != terminalcommand.ExitFailure {
				t.Fatalf("exit code = %d, want %d", code, terminalcommand.ExitFailure)
			}
		})
	}
	if code := terminalcommand.Execute(context.Background(), []string{"help"}, errorWriter{}, io.Discard, nil); code != terminalcommand.ExitFailure {
		t.Fatalf("help output failure code = %d", code)
	}
	if code := terminalcommand.Execute(context.Background(), []string{"invalid"}, io.Discard, errorWriter{}, nil); code != terminalcommand.ExitFailure {
		t.Fatalf("usage output failure code = %d", code)
	}
	if code := terminalcommand.Execute(context.Background(), []string{"help"}, shortWriter{}, io.Discard, nil); code != terminalcommand.ExitFailure {
		t.Fatalf("short help output code = %d", code)
	}
	runtimeCalled := false
	if code := terminalcommand.Execute(context.Background(), []string{"--check"}, io.Discard, errorWriter{},
		terminalcommand.RunnerFunc(func(context.Context, terminalcommand.Options) error {
			runtimeCalled = true
			return errors.New("runtime failed")
		})); code != terminalcommand.ExitFailure || !runtimeCalled {
		t.Fatalf("runtime output failure = code %d, called %v", code, runtimeCalled)
	}
	called := false
	if code := terminalcommand.Execute(context.Background(), nil, io.Discard, errorWriter{},
		terminalcommand.RunnerFunc(func(context.Context, terminalcommand.Options) error { called = true; return nil })); code != terminalcommand.ExitFailure || called {
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
