package daemoncommand

import "context"

type Runner interface {
	Run(context.Context, Options) error
}
