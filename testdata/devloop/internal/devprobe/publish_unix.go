//go:build !windows

package devprobe

import "os"

func replacePublishedAddress(source, destination string) error {
	// #nosec G703 -- both paths were constructed inside the validated test-owned directory.
	return os.Rename(source, destination)
}
