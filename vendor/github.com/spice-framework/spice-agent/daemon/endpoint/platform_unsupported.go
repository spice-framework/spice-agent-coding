//go:build !linux && !darwin && !windows

package endpoint

import "errors"

func validateCurrentPlatform(Metadata) error {
	return errors.New("local endpoint storage is unsupported on this platform")
}
