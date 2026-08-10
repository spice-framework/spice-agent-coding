package terminalcommand

import "context"

type Runner interface {
	Run(context.Context, Options) error
}
