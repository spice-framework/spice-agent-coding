package terminalcommand

import (
	"context"
	"errors"
)

type RunnerFunc func(context.Context, Options) error

func (run RunnerFunc) Run(ctx context.Context, options Options) error {
	if run == nil {
		return errors.New("terminal runner function is nil")
	}
	return run(ctx, options)
}
