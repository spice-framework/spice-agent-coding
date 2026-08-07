//go:build !linux && !darwin && !windows

package localclient

import (
	"errors"

	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

func validatePlatformTransport(endpoint.Transport) error {
	return errors.New("local endpoint transport is unsupported on this platform")
}
