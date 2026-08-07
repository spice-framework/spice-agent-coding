package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/terminal"
	"github.com/spice-framework/spice-agent-coding/internal/terminal"
	"github.com/spice-framework/spice-agent-coding/internal/terminalcommand"
	agenttui "github.com/spice-framework/spice-agent-tui"
	spiceconfig "github.com/spice-framework/spice/config"
)

type terminalApplication interface {
	Start(context.Context) error
	Stop(context.Context) error
	ShutdownTimeout() time.Duration
	Shell() agenttui.Shell
}

type generatedApplication struct {
	*spicegen.Application
}

func (application generatedApplication) Shell() agenttui.Shell {
	return application.Components().TerminalShell
}

type applicationFactory func(context.Context, spicegen.ApplicationOptions) (terminalApplication, error)

type generatedRunner struct {
	options        spicegen.ApplicationOptions
	newApplication applicationFactory
}

func (runner *generatedRunner) Run(ctx context.Context, options terminalcommand.Options) error {
	if runner == nil || runner.newApplication == nil || ctx == nil {
		return errors.New("generated terminal runner is unavailable")
	}
	applicationOptions, err := runner.optionsFor(options)
	if err != nil {
		return err
	}
	application, err := runner.newApplication(ctx, applicationOptions)
	if err != nil {
		return fmt.Errorf("construct generated terminal: %w", err)
	}
	if application == nil {
		return errors.New("generated terminal factory returned no application")
	}
	if options.Mode() == terminalcommand.ModeCheck {
		return stopTerminalApplication(application)
	}
	if options.Mode() != terminalcommand.ModeManaged && options.Mode() != terminalcommand.ModeAttach {
		return errors.Join(errors.New("generated terminal mode is unsupported"), stopTerminalApplication(application))
	}
	if err = application.Start(ctx); err != nil {
		return errors.Join(fmt.Errorf("start generated terminal: %w", err), stopTerminalApplication(application))
	}
	shell := application.Shell()
	if shell == nil {
		return errors.Join(errors.New("generated terminal shell is unavailable"), stopTerminalApplication(application))
	}
	runErr := shell.Run(ctx)
	if context.Cause(ctx) != nil && errors.Is(runErr, context.Cause(ctx)) {
		runErr = nil
	}
	return errors.Join(runErr, stopTerminalApplication(application))
}

func (runner *generatedRunner) optionsFor(options terminalcommand.Options) (spicegen.ApplicationOptions, error) {
	var mode string
	values := make(map[string]string, 2)
	switch options.Mode() {
	case terminalcommand.ModeManaged:
		mode = terminal.ModeManaged
	case terminalcommand.ModeAttach:
		mode = terminal.ModeAttach
		values["agent.terminal.endpoint"] = options.Endpoint()
	case terminalcommand.ModeCheck:
		mode = terminal.ModeCheck
	default:
		return spicegen.ApplicationOptions{}, errors.New("terminal command mode is unsupported")
	}
	values["agent.terminal.mode"] = mode
	invocation, err := spiceconfig.NewMapSource("terminal-command", values)
	if err != nil {
		return spicegen.ApplicationOptions{}, fmt.Errorf("construct terminal command configuration: %w", err)
	}
	result := runner.options
	result.Sources = append(slices.Clone(runner.options.Sources), invocation)
	return result, nil
}

func stopTerminalApplication(application terminalApplication) error {
	if application == nil {
		return errors.New("generated terminal application is unavailable")
	}
	timeout := application.ShutdownTimeout()
	if timeout <= 0 {
		return errors.New("generated terminal shutdown timeout is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := application.Stop(ctx); err != nil {
		return fmt.Errorf("stop generated terminal: %w", err)
	}
	return nil
}
