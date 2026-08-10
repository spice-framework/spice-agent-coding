//go:build linux || darwin

package daemonprocess

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

const descendantGateEnvironment = "SPICE_AGENT_DESCENDANT_GATE_FD"

// DescendantRegistration owns the child side of cooperative containment.
type DescendantRegistration struct{}

// Await is the first operation a daemon-owned child performs before user work.
func (DescendantRegistration) Await() error {
	value, exists := os.LookupEnv(descendantGateEnvironment)
	if !exists {
		return errors.New("managed daemon descendant gate is unavailable")
	}
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 {
		return errors.New("managed daemon descendant gate is invalid")
	}
	unix.CloseOnExec(fd)
	gate := os.NewFile(uintptr(fd), "managed-daemon-descendant-gate")
	if gate == nil {
		return errors.New("managed daemon descendant gate is invalid")
	}
	defer gate.Close() //nolint:errcheck // The protocol result is authoritative.
	if _, err = gate.Write([]byte{1}); err != nil {
		return fmt.Errorf("acknowledge managed daemon descendant gate: %w", err)
	}
	release := []byte{0}
	if _, err = io.ReadFull(gate, release); err != nil {
		return fmt.Errorf("await managed daemon descendant release: %w", err)
	}
	if release[0] != 1 {
		return errors.New("managed daemon descendant release is invalid")
	}
	return nil
}
