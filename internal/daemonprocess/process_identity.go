//go:build linux || darwin

package daemonprocess

type processIdentity struct {
	startedSeconds int64
	startedPart    uint64
}

func (identity processIdentity) isZero() bool {
	return identity.startedSeconds == 0 && identity.startedPart == 0
}
