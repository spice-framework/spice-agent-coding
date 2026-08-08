//go:build !spice_generate

package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spice-framework/spice-agent-coding/internal/distribution"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agent"
	"github.com/spice-framework/spice-agent-coding/internal/terminalcommand"
	agenttui "github.com/spice-framework/spice-agent-tui"
	spicebean "github.com/spice-framework/spice/bean"
	spiceconfig "github.com/spice-framework/spice/config"
)

func main() {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stopSignals()
	os.Exit(code) //nolint:forbidigo // The command entrypoint must propagate its parsed exit code.
}

func execute(ctx context.Context, arguments []string, input io.Reader, stdout, stderr io.Writer) int {
	if len(arguments) == 1 && arguments[0] == "--version" {
		if err := distribution.WriteVersion(stdout, distribution.TerminalComponent); err != nil {
			return terminalcommand.ExitFailure
		}
		return terminalcommand.ExitSuccess
	}
	environment, err := spiceconfig.OSEnvironment("SPICE_")
	if err != nil {
		_, _ = io.WriteString(stderr, "spice-agent: configuration is unavailable\n") //nolint:errcheck // Exit status remains authoritative.
		return terminalcommand.ExitFailure
	}
	streams, err := agenttui.NewTerminalIO(input, stdout)
	if err != nil {
		_, _ = io.WriteString(stderr, "spice-agent: terminal is unavailable\n") //nolint:errcheck // Exit status remains authoritative.
		return terminalcommand.ExitFailure
	}
	applicationOptions := spicegen.ApplicationOptions{
		Sources: []spiceconfig.Source{environment},
		Overrides: spicegen.BeanOverrides{
			OsTerminalIO: spicebean.Replace(streams),
		},
	}
	applicationOptions, err = acceptanceApplicationOptions(applicationOptions)
	if err != nil {
		_, _ = io.WriteString(stderr, "spice-agent: acceptance endpoint is unavailable\n") //nolint:errcheck // Exit status remains authoritative.
		return terminalcommand.ExitFailure
	}
	runner := &generatedRunner{
		options: applicationOptions,
		newApplication: func(
			callContext context.Context,
			options spicegen.ApplicationOptions,
		) (terminalApplication, error) {
			application, constructErr := spicegen.NewApplicationWithOptions(callContext, options)
			if constructErr != nil {
				return nil, constructErr
			}
			return generatedApplication{Application: application}, nil
		},
	}
	return terminalcommand.Execute(ctx, arguments, stdout, stderr, runner)
}
