package daemon

import (
	"context"
	"net"

	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

type runtimeServices struct {
	listen   func(string) (net.Listener, error)
	publish  func(context.Context, endpoint.Metadata) (runtimePublication, error)
	metadata func() (endpoint.Metadata, error)
}
