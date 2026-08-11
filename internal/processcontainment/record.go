//go:build linux || darwin

package processcontainment

// Record is one immutable process-table observation.
type Record struct {
	PID      int
	ParentID int
	GroupID  int
	Identity Identity
}
