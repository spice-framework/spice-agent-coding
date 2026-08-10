package daemonprocess

import "os"

// RootRegistry is the daemon-side lifecycle handle for optional supervisor
// descendant registration. Generated daemon entrypoints adopt it before any
// child-capable bean starts and close it during application cleanup.
type RootRegistry interface {
	Register(*os.Process) error
	Close() error
}
