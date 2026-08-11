//go:build !spice_generate

package main

import (
	"context"
	"io"

	"github.com/spice-framework/spice-agent-coding/internal/distribution"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agent"
	"github.com/spice-framework/spice-agent-coding/internal/terminalcommand"
	agenttui "github.com/spice-framework/spice-agent-tui"
	spicebean "github.com/spice-framework/spice/bean"
	spiceconfig "github.com/spice-framework/spice/config"
)

type command struct {
	input  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func (command command) execute(ctx context.Context, arguments []string) int {
	if len(arguments) == 1 && arguments[0] == "--version" {
		if err := distribution.NewBuild().WriteVersion(command.stdout, distribution.TerminalComponent); err != nil {
			return terminalcommand.ExitFailure
		}
		return terminalcommand.ExitSuccess
	}
	environment, err := spiceconfig.OSEnvironment("SPICE_")
	if err != nil {
		_, _ = io.WriteString(command.stderr, "spice-agent: configuration is unavailable\n") //nolint:errcheck // Exit status remains authoritative.
		return terminalcommand.ExitFailure
	}
	streams, err := agenttui.NewTerminalIO(command.input, command.stdout)
	if err != nil {
		_, _ = io.WriteString(command.stderr, "spice-agent: terminal is unavailable\n") //nolint:errcheck // Exit status remains authoritative.
		return terminalcommand.ExitFailure
	}
	applicationOptions := spicegen.ApplicationOptions{
		Sources: []spiceconfig.Source{environment},
		Overrides: spicegen.BeanOverrides{
			OsTerminalIO: spicebean.Replace(streams),
		},
	}
	applicationOptions, err = (acceptanceAdapter{}).applicationOptions(applicationOptions)
	if err != nil {
		_, _ = io.WriteString(command.stderr, "spice-agent: acceptance endpoint is unavailable\n") //nolint:errcheck // Exit status remains authoritative.
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
	return (terminalcommand.Command{}).Execute(ctx, arguments, command.stdout, command.stderr, runner)
}
