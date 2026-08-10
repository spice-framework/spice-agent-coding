package terminalconnector

import (
	"context"

	"github.com/spice-framework/spice-agent/client"
)

type explicitDiscovery interface {
	Discover(context.Context) (client.Connector, error)
	Close() error
}
