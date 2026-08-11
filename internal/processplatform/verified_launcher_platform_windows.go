//go:build windows

package processplatform

import (
	"context"
	"errors"

	agentprocess "github.com/spice-framework/spice-agent/process"
)

type verifiedLauncherPlatform struct{}

// Start rechecks the leased Windows file identity immediately before the
// suspended child is created. The caller-owned lease denies write/delete
// sharing until Start returns after Job assignment and thread resume.
func (verifiedLauncherPlatform) Start(
	ctx context.Context,
	launcher *Launcher,
	lease *agentprocess.ExecutableLease,
	spec agentprocess.Spec,
) (agentprocess.Process, error) {
	if launcher == nil || ctx == nil {
		return nil, agentprocess.NewFailure(
			agentprocess.OperationLaunch,
			errors.New("verified platform launcher is unavailable"),
		)
	}
	if err := lease.ValidateSpec(spec); err != nil {
		return nil, agentprocess.NewFailure(agentprocess.OperationLaunch, err)
	}
	if err := lease.Recheck(ctx); err != nil {
		return nil, agentprocess.NewFailure(agentprocess.OperationLaunch, err)
	}
	return launcher.Start(ctx, spec)
}
