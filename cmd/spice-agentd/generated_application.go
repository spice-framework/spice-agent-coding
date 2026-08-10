//go:build !spice_generate

package main

import spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agentd"

type generatedApplication struct {
	*spicegen.Application
}

func (application generatedApplication) RuntimeDone() <-chan struct{} {
	return application.Components().DaemonRuntime.Done()
}

func (application generatedApplication) RuntimeErr() error {
	return application.Components().DaemonRuntime.Err()
}
