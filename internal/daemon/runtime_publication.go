package daemon

import "context"

type runtimePublication interface {
	CloseContext(context.Context) error
}
