package daemonprocess

import (
	"errors"
	"os"
)

type inactiveRootRegistry struct{}

func (inactiveRootRegistry) Register(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return errors.New("managed daemon descendant registration is invalid")
	}
	return nil
}

func (inactiveRootRegistry) Close() error { return nil }
