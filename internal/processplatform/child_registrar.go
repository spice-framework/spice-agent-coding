package processplatform

import "os"

// ChildRegistrar records a successfully started direct child in the daemon's
// adopted root containment boundary. Implementations must not retain process.
type ChildRegistrar interface {
	Register(process *os.Process) error
}
