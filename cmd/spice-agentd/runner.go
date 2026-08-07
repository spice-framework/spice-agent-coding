//go:build !spice_generate

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spice-framework/spice-agent-coding/internal/daemoncommand"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
)

type daemonApplication interface {
	Start(context.Context) error
	Stop(context.Context) error
	ShutdownTimeout() time.Duration
	RuntimeDone() <-chan struct{}
	RuntimeErr() error
}

type generatedApplication struct {
	*spicegen.Application
}

func (application generatedApplication) RuntimeDone() <-chan struct{} {
	return application.Components().DaemonRuntime.Done()
}

func (application generatedApplication) RuntimeErr() error {
	return application.Components().DaemonRuntime.Err()
}

type applicationFactory func(context.Context, spicegen.ApplicationOptions) (daemonApplication, error)

type generatedRunner struct {
	options        spicegen.ApplicationOptions
	newApplication applicationFactory
}

func (runner *generatedRunner) Run(ctx context.Context, options daemoncommand.Options) error {
	if runner == nil || runner.newApplication == nil || ctx == nil {
		return errors.New("generated daemon runner is unavailable")
	}
	application, err := runner.newApplication(ctx, runner.options)
	if err != nil {
		return fmt.Errorf("construct generated daemon: %w", err)
	}
	if options.Mode() == daemoncommand.ModeCheck {
		return stopApplication(application)
	}
	if options.Mode() != daemoncommand.ModeServe {
		return errors.New("generated daemon mode is unsupported")
	}
	if err = application.Start(ctx); err != nil {
		return errors.Join(fmt.Errorf("start generated daemon: %w", err), stopApplication(application))
	}

	select {
	case <-ctx.Done():
		err = nil
	case <-application.RuntimeDone():
		err = application.RuntimeErr()
		if err == nil {
			err = errors.New("generated daemon transport stopped unexpectedly")
		}
	}
	return errors.Join(err, stopApplication(application))
}

func stopApplication(application daemonApplication) error {
	if application == nil {
		return errors.New("generated daemon application is unavailable")
	}
	timeout := application.ShutdownTimeout()
	if timeout <= 0 {
		return errors.New("generated daemon shutdown timeout is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := application.Stop(ctx); err != nil {
		return fmt.Errorf("stop generated daemon: %w", err)
	}
	return nil
}
