package processplatform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	agentprocess "github.com/spice-framework/spice-agent/process"
)

// ChildRegistrar records a successfully started direct child in the daemon's
// adopted root containment boundary. Implementations must not retain process.
type ChildRegistrar interface {
	Register(process *os.Process) error
}

type platformStart func(context.Context, agentprocess.Spec, ChildRegistrar) (agentprocess.Process, error)

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
	return &Launcher{registrar: registrar, start: startPlatformProcess}, nil
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

func operationContext(ctx context.Context, operation agentprocess.Operation) error {
	if ctx == nil {
		return agentprocess.NewFailure(operation, errors.New("process operation context is required"))
	}
	if cause := context.Cause(ctx); cause != nil {
		return agentprocess.NewFailure(operation, cause)
	}
	return nil
}

type terminalContainmentError struct{ cause error }

func (*terminalContainmentError) Error() string   { return "platform process containment cleanup failed" }
func (*terminalContainmentError) Retryable() bool { return false }
func (failure *terminalContainmentError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func terminalContainmentFailure(cause error) error {
	if cause == nil {
		return nil
	}
	return agentprocess.NewFailure(
		agentprocess.OperationWait,
		&terminalContainmentError{cause: cause},
	)
}

func (*Launcher) String() string            { return "processplatform.Launcher([REDACTED])" }
func (launcher *Launcher) GoString() string { return launcher.String() }
func (*Launcher) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "processplatform.Launcher([REDACTED])") //nolint:errcheck // fmt.Formatter cannot return an error.
}
func (launcher *Launcher) LogValue() slog.Value { return slog.StringValue(launcher.String()) }

func (*Resolver) String() string            { return "processplatform.Resolver([REDACTED])" }
func (resolver *Resolver) GoString() string { return resolver.String() }
func (*Resolver) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "processplatform.Resolver([REDACTED])") //nolint:errcheck // fmt.Formatter cannot return an error.
}
func (resolver *Resolver) LogValue() slog.Value { return slog.StringValue(resolver.String()) }

var _ agentprocess.Launcher = (*Launcher)(nil)
