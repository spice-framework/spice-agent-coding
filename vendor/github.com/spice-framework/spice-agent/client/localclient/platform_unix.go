//go:build linux || darwin

package localclient

import (
	"fmt"

	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

func validatePlatformTransport(transport endpoint.Transport) error {
	if transport != endpoint.TransportUnixSocket {
		return fmt.Errorf("local endpoint transport %q is unsupported on this platform", transport)
	}
	return nil
}
