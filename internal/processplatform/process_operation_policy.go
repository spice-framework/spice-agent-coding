package processplatform

import (
	"context"
	"errors"

	agentprocess "github.com/spice-framework/spice-agent/process"
)

type processOperationPolicy struct{}

func (processOperationPolicy) context(ctx context.Context, operation agentprocess.Operation) error {
	if ctx == nil {
		return agentprocess.NewFailure(operation, errors.New("process operation context is required"))
	}
	if cause := context.Cause(ctx); cause != nil {
		return agentprocess.NewFailure(operation, cause)
	}
	return nil
}

func (processOperationPolicy) terminalContainmentFailure(cause error) error {
	if cause == nil {
		return nil
	}
	return agentprocess.NewFailure(
		agentprocess.OperationWait,
		&terminalContainmentError{cause: cause},
	)
}
