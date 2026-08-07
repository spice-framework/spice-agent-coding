package daemon

import (
	"errors"
	"fmt"
)

var (
	// ErrStaleSession rejects a client ownership epoch that lost its CAS.
	ErrStaleSession = errors.New("daemon session ownership is stale")
	// ErrSessionStoreClosed rejects work after daemon-root shutdown.
	ErrSessionStoreClosed = errors.New("daemon session store is closed")
	// ErrSessionGateCapacity rejects work that would exceed a bounded
	// per-client mutation/reconnect queue or stream lease set.
	ErrSessionGateCapacity = errors.New("daemon session gate capacity exhausted")
)

// StaleSessionError reports the authoritative and presented ownership epochs
// for a known stable client. Unknown client identities continue to return the
// ErrStaleSession sentinel because no positive authoritative epoch exists.
type StaleSessionError struct {
	clientID string
	expected uint64
	observed uint64
}

func (failure *StaleSessionError) Error() string {
	if failure == nil {
		return ErrStaleSession.Error()
	}
	return fmt.Sprintf("%s: expected epoch %d, observed epoch %d", ErrStaleSession, failure.expected, failure.observed)
}

// Is makes StaleSessionError match ErrStaleSession.
func (failure *StaleSessionError) Is(target error) bool { return target == ErrStaleSession }

// ClientID returns the stable client whose ownership check failed.
func (failure *StaleSessionError) ClientID() string {
	if failure == nil {
		return ""
	}
	return failure.clientID
}

// ExpectedEpoch returns the daemon's current positive epoch.
func (failure *StaleSessionError) ExpectedEpoch() uint64 {
	if failure == nil {
		return 0
	}
	return failure.expected
}

// ObservedEpoch returns the epoch supplied by the caller.
func (failure *StaleSessionError) ObservedEpoch() uint64 {
	if failure == nil {
		return 0
	}
	return failure.observed
}

// SessionGateCapacityError identifies the exhausted per-client gate resource.
type SessionGateCapacityError struct {
	resource string
	maximum  int
}

func (failure *SessionGateCapacityError) Error() string {
	if failure == nil {
		return ErrSessionGateCapacity.Error()
	}
	return fmt.Sprintf("%s: %s maximum %d", ErrSessionGateCapacity, failure.resource, failure.maximum)
}

// Is makes SessionGateCapacityError match ErrSessionGateCapacity.
func (failure *SessionGateCapacityError) Is(target error) bool {
	return target == ErrSessionGateCapacity
}

// Resource returns the bounded resource that was exhausted.
func (failure *SessionGateCapacityError) Resource() string {
	if failure == nil {
		return ""
	}
	return failure.resource
}

// Maximum returns the configured hard maximum for the resource.
func (failure *SessionGateCapacityError) Maximum() int {
	if failure == nil {
		return 0
	}
	return failure.maximum
}

func staleSession(clientID string, expected, observed uint64) error {
	return &StaleSessionError{clientID: clientID, expected: expected, observed: observed}
}

func newSessionGateCapacity(resource string, maximum int) error {
	return &SessionGateCapacityError{resource: resource, maximum: maximum}
}
