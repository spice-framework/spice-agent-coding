//go:build linux || darwin

package localendpoint

import (
	"encoding/base32"
	"path/filepath"
	"strings"

	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice-agent/daemon/localipc"
)

const maximumUnixSocketPathBytes = 100

func derivePlatformAddress(scope endpoint.UserScope, identity launchIdentity) (string, error) {
	if scope.Transport() != endpoint.TransportUnixSocket {
		return "", ErrUnavailable
	}
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(identity[:]))
	return deriveUnixAddress(scope.Directory(), "p-"+encoded)
}

func deriveUnixAddress(directory, name string) (string, error) {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory ||
		!safeUnixName(name) {
		return "", ErrUnavailable
	}
	address := filepath.Join(directory, name)
	if len(address) > maximumUnixSocketPathBytes {
		return "", ErrUnavailable
	}
	return address, nil
}

func safeUnixName(name string) bool {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || len(name) > 128 {
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

func cleanupPlatformAddress(address string) error {
	listener, err := localipc.Listen(address)
	if err != nil {
		return err
	}
	return listener.Close()
}
