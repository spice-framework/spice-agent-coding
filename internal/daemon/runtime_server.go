package daemon

import (
	"context"
	"net"
)

type runtimeServer interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
}
