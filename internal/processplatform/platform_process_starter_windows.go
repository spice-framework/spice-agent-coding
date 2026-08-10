//go:build windows

package processplatform

import (
	"context"

	agentprocess "github.com/spice-framework/spice-agent/process"
)

type platformProcessStarter struct{}

func (platformProcessStarter) Start(
	ctx context.Context,
	spec agentprocess.Spec,
	registrar ChildRegistrar,
) (agentprocess.Process, error) {
	return (&windowsProcess{}).start(ctx, spec, registrar)
}
