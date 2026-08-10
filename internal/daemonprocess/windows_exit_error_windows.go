//go:build windows

package daemonprocess

import "fmt"

type windowsExitError struct{ code uint32 }

func (failure *windowsExitError) Error() string {
	return fmt.Sprintf("managed daemon exited with status %d", failure.code)
}

func (failure *windowsExitError) ExitCode() uint32 {
	if failure == nil {
		return 0
	}
	return failure.code
}
