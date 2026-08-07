//go:build !windows && !linux && !darwin

package localendpoint

import (
	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

func derivePlatformAddress(endpoint.UserScope, launchIdentity) (string, error) {
	return "", ErrUnavailable
}

func cleanupPlatformAddress(string) error { return ErrCleanup }
