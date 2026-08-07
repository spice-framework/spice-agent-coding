//go:build !spice_generate

package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spice-framework/spice-agent-coding/internal/daemoncommand"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
	spiceconfig "github.com/spice-framework/spice/config"
)

func main() {
	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, stopParentControl := withParentControl(signalContext, os.Stdin, isTerminal(os.Stdin))
	code := execute(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stopParentControl()
	stopSignals()
	os.Exit(code) //nolint:forbidigo // The command entrypoint must propagate its parsed exit code.
}

func execute(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	environment, err := spiceconfig.OSEnvironment("SPICE_")
	if err != nil {
		if _, writeErr := io.WriteString(stderr, "spice-agentd: configuration is unavailable\n"); writeErr != nil {
			return daemoncommand.ExitFailure
		}
		return daemoncommand.ExitFailure
	}
	runner := &generatedRunner{
		options: spicegen.ApplicationOptions{Sources: []spiceconfig.Source{environment}},
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
	return daemoncommand.Execute(ctx, arguments, stdout, stderr, runner)
}

func withParentControl(parent context.Context, input io.Reader, terminal bool) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if input == nil || terminal {
		return ctx, cancel
	}
	go func() {
		// Managed stdin carries no application data. Drain it so any bytes
		// preceding EOF cannot disable the parent-death signal. A read failure
		// is also a lost control channel and therefore fails closed.
		if _, err := io.Copy(io.Discard, input); err != nil {
			cancel()
			return
		}
		cancel()
	}()
	return ctx, cancel
}

func isTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil || info == nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
