//go:build linux || darwin

package processplatform

import (
	"context"
	"errors"

	agentprocess "github.com/spice-framework/spice-agent/process"
)

type verifiedLauncherPlatform struct{}

// Start launches a private digest-verified executable snapshot. The
// configured path is never reopened after the caller acquires its lease.
func (verifiedLauncherPlatform) Start(
	ctx context.Context,
	launcher *Launcher,
	lease *agentprocess.ExecutableLease,
	spec agentprocess.Spec,
) (owned agentprocess.Process, resultErr error) {
	if launcher == nil || ctx == nil {
		return nil, agentprocess.NewFailure(
			agentprocess.OperationLaunch,
			errors.New("verified platform launcher is unavailable"),
		)
	}
	if err := lease.ValidateSpec(spec); err != nil {
		return nil, agentprocess.NewFailure(agentprocess.OperationLaunch, err)
	}
	materialized, err := lease.MaterializeForLaunch(ctx)
	if err != nil {
		return nil, agentprocess.NewFailure(agentprocess.OperationLaunch, err)
	}
	defer func() {
		if closeErr := materialized.Close(); closeErr != nil {
			resultErr = agentprocess.NewFailure(
				agentprocess.OperationLaunch,
				errors.Join(resultErr, closeErr),
			)
		}
	}()
	launchSpec, err := agentprocess.NewSpec(agentprocess.Config{
		Executable: materialized.Path(), Arguments: spec.Arguments(),
		WorkingDirectory: spec.WorkingDirectory(), Environment: spec.Environment(),
		Stdin: spec.Stdin(), Stdout: spec.Stdout(), Stderr: spec.Stderr(),
		Capabilities: spec.Capabilities(),
	})
	if err != nil {
		return nil, agentprocess.NewFailure(agentprocess.OperationLaunch, err)
	}
	if err = materialized.Recheck(ctx); err != nil {
		return nil, agentprocess.NewFailure(agentprocess.OperationLaunch, err)
	}
	return launcher.Start(ctx, launchSpec)
}
