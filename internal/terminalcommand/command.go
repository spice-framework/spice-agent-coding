// Package terminalcommand defines the transport-neutral command boundary for
// the Spice Agent terminal. Client and managed-daemon behavior is injected
// through Runner and remains outside this package.
package terminalcommand

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Exit codes are the complete command outcome contract.
const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const (
	capabilityWarning = "WARNING: coding tools run with your user account's process and filesystem privileges; no sandbox or permission prompt is active.\n"
	runtimeFailure    = "spice-agent: operation failed; see protected diagnostics for details\n"
	invalidArguments  = "spice-agent: invalid arguments or non-local endpoint\n"
	usage             = "Usage:\n  spice-agent\n  spice-agent attach --endpoint <local-endpoint>\n  spice-agent --check\n  spice-agent help\n\n" + capabilityWarning
)

// Mode identifies the one operation a terminal Runner must perform.
type Mode uint8

const (
	// ModeManaged attaches to a compatible local daemon or starts an owned one.
	ModeManaged Mode = iota + 1
	// ModeAttach attaches to the explicitly supplied local endpoint.
	ModeAttach
	// ModeCheck validates construction and configuration without attaching.
	ModeCheck
)

// Options is an immutable parsed terminal invocation. It preserves the exact
// accepted endpoint and arguments without normalizing either value.
type Options struct {
	mode      Mode
	endpoint  string
	arguments []string
}

// Mode returns the selected terminal operation.
func (options Options) Mode() Mode {
	return options.mode
}

// Endpoint returns the exact endpoint supplied for ModeAttach.
func (options Options) Endpoint() string {
	return options.endpoint
}

// Arguments returns a copy of the exact accepted command arguments.
func (options Options) Arguments() []string {
	return slices.Clone(options.arguments)
}

// Runner executes an already validated terminal operation. Implementations own
// clients, managed processes, and generated applications.
type Runner interface {
	Run(context.Context, Options) error
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(context.Context, Options) error

// Run executes the function.
func (run RunnerFunc) Run(ctx context.Context, options Options) error {
	if run == nil {
		return errors.New("terminal runner function is nil")
	}
	return run(ctx, options)
}

// Execute parses arguments and invokes runner with the caller-owned context.
// It never includes arguments, endpoints, or runner error text in failures.
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
	if options.Mode() != ModeCheck && !write(stderr, capabilityWarning) {
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
	case len(exact) == 0:
		return Options{mode: ModeManaged, arguments: exact}, false, true
	case len(exact) == 1 && exact[0] == "--check":
		return Options{mode: ModeCheck, arguments: exact}, false, true
	case len(exact) == 1 && (exact[0] == "help" || exact[0] == "--help" || exact[0] == "-h"):
		return Options{}, true, true
	case len(exact) == 3 && exact[0] == "attach" && exact[1] == "--endpoint" && localOpaqueEndpoint(exact[2]):
		return Options{mode: ModeAttach, endpoint: exact[2], arguments: exact}, false, true
	default:
		return Options{}, false, false
	}
}

// localOpaqueEndpoint is intentionally not operating-system endpoint
// validation. It only rejects empty/control-containing values and forms that
// clearly request a network authority. The eventual transport validates the
// accepted opaque value for its platform.
func localOpaqueEndpoint(endpoint string) bool {
	if endpoint == "" || len(endpoint) > 4096 || !utf8.ValidString(endpoint) || strings.TrimSpace(endpoint) != endpoint {
		return false
	}
	for _, value := range endpoint {
		if unicode.IsControl(value) {
			return false
		}
	}
	lower := strings.ToLower(endpoint)
	if strings.Contains(lower, "://") {
		return false
	}
	for _, prefix := range []string{"dns:", "grpc:", "http:", "https:", "tcp:"} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	if strings.HasPrefix(endpoint, "//") {
		return false
	}
	if strings.HasPrefix(endpoint, `\\`) && !strings.HasPrefix(lower, `\\.\pipe\`) {
		return false
	}
	return !looksLikeHostPort(endpoint)
}

func looksLikeHostPort(endpoint string) bool {
	if len(endpoint) >= 3 && endpoint[1] == ':' && asciiLetter(endpoint[0]) &&
		(endpoint[2] == '\\' || endpoint[2] == '/') {
		return false
	}
	separator := strings.LastIndexByte(endpoint, ':')
	if separator < 0 {
		return false
	}
	host := endpoint[:separator]
	port := endpoint[separator+1:]
	if host == "" {
		return port != ""
	}
	if strings.ContainsAny(host, `/\`) {
		return strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]")
	}
	return true
}

func asciiLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
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
