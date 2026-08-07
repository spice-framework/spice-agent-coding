// Package daemoncommand defines the transport-neutral command boundary for the
// Spice Agent daemon. Transport construction is injected through Runner so the
// package cannot acquire a daemon, listener, or generated application itself.
package daemoncommand

import (
	"context"
	"errors"
	"io"
	"slices"
)

// Exit codes are the complete command outcome contract.
const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const (
	capabilityWarning = "WARNING: coding tools run with your user account's process and filesystem privileges; no sandbox or permission prompt is active.\n"
	runtimeFailure    = "spice-agentd: operation failed; see the daemon's protected diagnostics for details\n"
	invalidArguments  = "spice-agentd: invalid arguments\n"
	usage             = "Usage:\n  spice-agentd serve\n  spice-agentd --check\n  spice-agentd help\n\n" + capabilityWarning
)

// Mode identifies the one operation a daemon Runner must perform.
type Mode uint8

const (
	// ModeServe runs the local daemon until the caller's context is cancelled.
	ModeServe Mode = iota + 1
	// ModeCheck validates construction and configuration without serving.
	ModeCheck
)

// Options is an immutable parsed daemon invocation. Arguments returns a copy
// so a Runner cannot mutate command state retained by its caller.
type Options struct {
	mode      Mode
	arguments []string
}

// Mode returns the selected daemon operation.
func (options Options) Mode() Mode {
	return options.mode
}

// Arguments returns the exact, unnormalized arguments accepted by the parser.
func (options Options) Arguments() []string {
	return slices.Clone(options.arguments)
}

// Runner executes an already validated daemon operation. Implementations own
// transport and generated-application composition; this command package does
// not know either contract.
type Runner interface {
	Run(context.Context, Options) error
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(context.Context, Options) error

// Run executes the function.
func (run RunnerFunc) Run(ctx context.Context, options Options) error {
	if run == nil {
		return errors.New("daemon runner function is nil")
	}
	return run(ctx, options)
}

// Execute parses arguments and invokes runner with the caller-owned context.
// It never includes arguments or runner error text in user-visible failures.
func Execute(
	ctx context.Context,
	arguments []string,
	stdout io.Writer,
	stderr io.Writer,
	runner Runner,
) int {
	stdout = nonNilWriter(stdout)
	stderr = nonNilWriter(stderr)
	options, help, valid := parse(arguments)
	if help {
		if !write(stdout, usage) {
			return ExitFailure
		}
		return ExitSuccess
	}
	if !valid {
		if !write(stderr, invalidArguments) || !write(stderr, usage) {
			return ExitFailure
		}
		return ExitUsage
	}
	if ctx == nil || runner == nil || ctx.Err() != nil {
		if !write(stderr, runtimeFailure) {
			return ExitFailure
		}
		return ExitFailure
	}
	if options.Mode() == ModeServe && !write(stderr, capabilityWarning) {
		return ExitFailure
	}
	if runnerFailed(ctx, runner, options) {
		if !write(stderr, runtimeFailure) {
			return ExitFailure
		}
		return ExitFailure
	}
	return ExitSuccess
}

func runnerFailed(ctx context.Context, runner Runner, options Options) (failed bool) {
	defer func() {
		if recover() != nil {
			failed = true
		}
	}()
	return runner.Run(ctx, options) != nil
}

func parse(arguments []string) (Options, bool, bool) {
	exact := slices.Clone(arguments)
	switch {
	case len(exact) == 1 && exact[0] == "serve":
		return Options{mode: ModeServe, arguments: exact}, false, true
	case len(exact) == 1 && exact[0] == "--check":
		return Options{mode: ModeCheck, arguments: exact}, false, true
	case len(exact) == 1 && (exact[0] == "help" || exact[0] == "--help" || exact[0] == "-h"):
		return Options{}, true, true
	default:
		return Options{}, false, false
	}
}

func nonNilWriter(writer io.Writer) io.Writer {
	if writer == nil {
		return io.Discard
	}
	return writer
}

func write(writer io.Writer, value string) bool {
	written, err := io.WriteString(writer, value)
	return err == nil && written == len(value)
}
