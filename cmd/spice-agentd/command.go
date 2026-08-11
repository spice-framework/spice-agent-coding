//go:build !spice_generate

package main

import (
	"context"
	"io"
	"os"

	"github.com/spice-framework/spice-agent-coding/internal/daemoncommand"
	"github.com/spice-framework/spice-agent-coding/internal/distribution"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
	spiceconfig "github.com/spice-framework/spice/config"
)

type command struct {
	input  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func (command command) execute(ctx context.Context, arguments []string) int {
	if len(arguments) == 1 && arguments[0] == "--version" {
		if err := distribution.WriteVersion(command.stdout, distribution.DaemonComponent); err != nil {
			return daemoncommand.ExitFailure
		}
		return daemoncommand.ExitSuccess
	}
	environment, err := spiceconfig.OSEnvironment("SPICE_")
	if err != nil {
		if _, writeErr := io.WriteString(command.stderr, "spice-agentd: configuration is unavailable\n"); writeErr != nil {
			return daemoncommand.ExitFailure
		}
		return daemoncommand.ExitFailure
	}
	loggingWriter := command.stderr
	if loggingWriter == nil {
		loggingWriter = io.Discard
	}
	applicationOptions := spicegen.ApplicationOptions{
		Sources: []spiceconfig.Source{environment},
		Logging: &spicegen.LoggingOptions{Writer: loggingWriter},
	}
	adapter := acceptanceAdapter{}
	applicationOptions, err = adapter.applicationOptions(applicationOptions)
	if err != nil {
		if _, writeErr := io.WriteString(command.stderr, "spice-agentd: acceptance transport is unavailable\n"); writeErr != nil {
			return daemoncommand.ExitFailure
		}
		return daemoncommand.ExitFailure
	}
	runner := &generatedRunner{
		options: applicationOptions,
		newApplication: func(
			callContext context.Context,
			options spicegen.ApplicationOptions,
		) (daemonApplication, error) {
			application, constructErr := spicegen.NewApplicationWithOptions(callContext, options)
			if constructErr != nil {
				return nil, constructErr
			}
			return generatedApplication{Application: application}, nil
		},
	}
	return (daemoncommand.Command{}).Execute(ctx, arguments, command.stdout, command.stderr, adapter.daemonRunner(runner))
}

func (command command) withParentControl(
	parent context.Context,
	terminal bool,
) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if command.input == nil || terminal {
		return ctx, cancel
	}
	go func() {
		// Managed stdin carries no application data. Drain it so any bytes
		// preceding EOF cannot disable the parent-death signal. A read failure
		// is also a lost control channel and therefore fails closed.
		if _, err := io.Copy(io.Discard, command.input); err != nil {
			cancel()
			return
		}
		cancel()
	}()
	return ctx, cancel
}

func (command command) isTerminal() bool {
	file, available := command.input.(*os.File)
	if !available || file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil || info == nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
