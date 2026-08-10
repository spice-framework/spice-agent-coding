package daemon

import (
	"net"

	"github.com/spice-framework/spice-agent/daemon/localipc"
)

type localListenerFactory struct{}

func (localListenerFactory) Listen(address string) (net.Listener, error) {
	return localipc.Listen(address)
}
