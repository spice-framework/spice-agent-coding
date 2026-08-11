package processplatform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	agentprocess "github.com/spice-framework/spice-agent/process"
)

// Launcher starts arbitrary validated process specifications inside the
// strongest unprivileged process-tree boundary available on the host OS.
type Launcher struct {
	registrar ChildRegistrar
	start     platformStart
}

// NewLauncher constructs the distribution's native process launcher.
func NewLauncher(registrar ChildRegistrar) (*Launcher, error) {
	if registrar == nil {
		return nil, errors.New("process child registrar is required")
	}
	return &Launcher{registrar: registrar, start: (platformProcessStarter{}).Start}, nil
}

// Start consumes an immutable specification without consulting ambient
// environment state or invoking a shell.
func (launcher *Launcher) Start(ctx context.Context, spec agentprocess.Spec) (agentprocess.Process, error) {
	if launcher == nil || launcher.start == nil || launcher.registrar == nil {
		return nil, agentprocess.NewFailure(agentprocess.OperationLaunch, errors.New("platform launcher is unavailable"))
	}
	if ctx == nil {
		return nil, agentprocess.NewFailure(agentprocess.OperationLaunch, errors.New("launch context is required"))
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, agentprocess.NewFailure(agentprocess.OperationLaunch, cause)
	}
	if err := spec.Validate(); err != nil {
		return nil, agentprocess.NewFailure(agentprocess.OperationLaunch, err)
	}
	owned, err := launcher.start(ctx, spec.Clone(), launcher.registrar)
	if owned == nil {
		if err == nil {
			err = errors.New("platform launcher returned no owned process")
		}
		return nil, agentprocess.NewFailure(agentprocess.OperationLaunch, err)
	}
	if err != nil {
		return owned, agentprocess.NewFailure(agentprocess.OperationLaunch, err)
	}
	if cause := context.Cause(ctx); cause != nil {
		return owned, agentprocess.NewFailure(agentprocess.OperationLaunch, cause)
	}
	return owned, nil
}

// StartVerified launches only from an executable lease whose identity and
// digest remain authoritative through the platform-specific launch boundary.
func (launcher *Launcher) StartVerified(
	ctx context.Context,
	lease *agentprocess.ExecutableLease,
	spec agentprocess.Spec,
) (agentprocess.Process, error) {
	return (verifiedLauncherPlatform{}).Start(ctx, launcher, lease, spec)
}

func (*Launcher) String() string            { return "processplatform.Launcher([REDACTED])" }
func (launcher *Launcher) GoString() string { return launcher.String() }
func (*Launcher) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "processplatform.Launcher([REDACTED])") //nolint:errcheck // fmt.Formatter cannot return an error.
}
func (launcher *Launcher) LogValue() slog.Value { return slog.StringValue(launcher.String()) }
