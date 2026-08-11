//go:build linux || darwin

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
