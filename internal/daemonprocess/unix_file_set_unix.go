//go:build linux || darwin

package daemonprocess

import (
	"errors"
	"io"
	"os"
)

type unixFileSet struct{}

func (unixFileSet) close(files ...io.Closer) error {
	var failures []error
	for _, file := range files {
		if file == nil {
			continue
		}
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
