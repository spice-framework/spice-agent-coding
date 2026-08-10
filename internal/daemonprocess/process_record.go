//go:build linux || darwin

package daemonprocess

type processRecord struct {
	pid      int
	ppid     int
	pgid     int
	identity processIdentity
}
