//go:build windows

package localendpoint

import (
	"encoding/hex"
	"strings"

	"github.com/spice-framework/spice-agent/daemon/endpoint"
)

const (
	windowsPipePrefix        = `\\.\pipe\`
	windowsMaximumPipeBytes  = 256
	windowsMaximumNameBytes  = 128
	windowsPluginNameSegment = "-p-"
)

func derivePlatformAddress(scope endpoint.UserScope, identity launchIdentity) (string, error) {
	if scope.Transport() != endpoint.TransportWindowsNamedPipe {
		return "", ErrUnavailable
	}
	return deriveWindowsAddress(scope.Address(), hex.EncodeToString(identity[:]))
}

func deriveWindowsAddress(baseAddress, identity string) (string, error) {
	if !strings.HasPrefix(baseAddress, windowsPipePrefix) {
		return "", ErrUnavailable
	}
	address := baseAddress + windowsPluginNameSegment + identity
	name := strings.TrimPrefix(address, windowsPipePrefix)
	if len(address) > windowsMaximumPipeBytes || len(name) > windowsMaximumNameBytes ||
		!safeWindowsName(name) {
		return "", ErrUnavailable
	}
	return address, nil
}

func safeWindowsName(name string) bool {
	if !strings.HasPrefix(name, "spice-agent-") || name == "spice-agent-" {
		return false
	}
	for _, current := range name {
		if current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' ||
			current >= '0' && current <= '9' || strings.ContainsRune("._-", current) {
			continue
		}
		return false
	}
	return true
}

func cleanupPlatformAddress(string) error { return nil }
