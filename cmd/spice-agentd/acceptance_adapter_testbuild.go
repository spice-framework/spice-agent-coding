//go:build spice_acceptance && !spice_generate

package main

import (
	"context"
	"errors"
	"os"

	"github.com/spice-framework/spice-agent-coding/internal/daemon"
	"github.com/spice-framework/spice-agent-coding/internal/daemoncommand"
	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice-agent/model"
	spicebean "github.com/spice-framework/spice/bean"
)

type acceptanceAdapter struct{}

func (acceptanceAdapter) applicationOptions(
	options spicegen.ApplicationOptions,
) (spicegen.ApplicationOptions, error) {
	environment := acceptanceEnvironment{}
	directory := environment.scopeDirectory()
	if directory == "" {
		return spicegen.ApplicationOptions{}, errors.New("acceptance endpoint scope directory is required")
	}
	scope, err := endpoint.AcceptanceUserScope(directory)
	if err != nil {
		return spicegen.ApplicationOptions{}, err
	}
	options.Overrides.EndpointScope = spicebean.Replace(scope)

	trigger := environment.faultTrigger()
	ack := environment.faultAcknowledgement()
	if trigger == "" || ack == "" {
		if trigger != "" || ack != "" {
			return spicegen.ApplicationOptions{}, errors.New("acceptance fault trigger and acknowledgement must be configured together")
		}
	} else {
		override := &faultingListenerFactory{trigger: trigger, ack: ack}
		options.Overrides.DaemonListenerFactory = spicebean.Replace[daemon.ListenerFactory](override)
	}
	configuration := environment.providerConfiguration()
	if configuration.configured() {
		if !configuration.valid() {
			return spicegen.ApplicationOptions{}, errors.New("acceptance provider configuration is invalid")
		}
		provider := newAcceptanceProvider(configuration)
		options.Overrides.OpenAIModelProvider = spicebean.Replace[model.Provider](provider)
	}
	return options, nil
}

func (acceptanceAdapter) daemonRunner(
	runner daemoncommand.Runner,
) daemoncommand.Runner {
	path := (acceptanceEnvironment{}).diagnosticPath()
	if path == "" {
		return runner
	}
	return daemoncommand.RunnerFunc(func(ctx context.Context, options daemoncommand.Options) error {
		err := runner.Run(ctx, options)
		if err != nil {
			_ = os.WriteFile(path, []byte(err.Error()+"\n"), 0o600)
		}
		return err
	})
}
