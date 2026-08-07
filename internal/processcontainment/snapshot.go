//go:build linux || darwin

// Package processcontainment provides identity-bearing process-table snapshots
// shared by the managed daemon supervisor and arbitrary process launcher.
package processcontainment

// Identity distinguishes PID reuse using the strongest process birth value
// exposed by the host platform.
type Identity struct {
	StartedSeconds int64
	StartedPart    uint64
}

// IsZero reports whether an identity could not be anchored.
func (identity Identity) IsZero() bool {
	return identity.StartedSeconds == 0 && identity.StartedPart == 0
}

// Record is one immutable process-table observation.
type Record struct {
	PID      int
	ParentID int
	GroupID  int
	Identity Identity
}
