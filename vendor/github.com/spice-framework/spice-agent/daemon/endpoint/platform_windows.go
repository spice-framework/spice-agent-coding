//go:build windows

package endpoint

import "fmt"

func validateCurrentPlatform(metadata Metadata) error {
	if metadata.transport != TransportWindowsNamedPipe {
		return fmt.Errorf("local endpoint transport %q is not supported on this platform", metadata.transport)
	}
	return nil
}
