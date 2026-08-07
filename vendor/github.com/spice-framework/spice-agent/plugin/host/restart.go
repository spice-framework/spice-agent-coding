package pluginhost

import (
	"errors"
	"time"
)

// MaximumRestartAttempts is the hard bound for one automatic-recovery episode.
const MaximumRestartAttempts uint32 = 8

// RestartPolicy is an immutable, bounded automatic-recovery policy. Its zero
// value disables recovery. Recovery always replaces a complete plugin Set; it
// never replays an in-flight tool call or repairs one process in place.
type RestartPolicy struct {
	maximumAttempts uint32
	initialBackoff  time.Duration
	maximumBackoff  time.Duration
	attemptTimeout  time.Duration
}

// NewRestartPolicy constructs a bounded automatic-recovery policy.
func NewRestartPolicy(
	maximumAttempts uint32,
	initialBackoff time.Duration,
	maximumBackoff time.Duration,
	attemptTimeout time.Duration,
) (RestartPolicy, error) {
	policy := RestartPolicy{
		maximumAttempts: maximumAttempts,
		initialBackoff:  initialBackoff,
		maximumBackoff:  maximumBackoff,
		attemptTimeout:  attemptTimeout,
	}
	if err := policy.Validate(); err != nil {
		return RestartPolicy{}, err
	}
	return policy, nil
}

// DefaultRestartPolicy returns the conservative production default.
func DefaultRestartPolicy() RestartPolicy {
	return RestartPolicy{
		maximumAttempts: 3,
		initialBackoff:  250 * time.Millisecond,
		maximumBackoff:  time.Second,
		attemptTimeout:  30 * time.Second,
	}
}

// Validate rejects partial, unbounded, and internally inconsistent policies.
func (policy RestartPolicy) Validate() error {
	if policy == (RestartPolicy{}) {
		return nil
	}
	if policy.maximumAttempts == 0 || policy.maximumAttempts > MaximumRestartAttempts {
		return errors.New("runtime plugin restart maximum attempts is out of range")
	}
	if policy.initialBackoff <= 0 || policy.initialBackoff > MaximumOperationTimeout {
		return errors.New("runtime plugin restart initial backoff is out of range")
	}
	if policy.maximumBackoff < policy.initialBackoff || policy.maximumBackoff > MaximumOperationTimeout {
		return errors.New("runtime plugin restart maximum backoff is out of range")
	}
	if policy.attemptTimeout <= 0 || policy.attemptTimeout > MaximumOperationTimeout {
		return errors.New("runtime plugin restart attempt timeout is out of range")
	}
	return nil
}

// Enabled reports whether automatic recovery is configured.
func (policy RestartPolicy) Enabled() bool { return policy.maximumAttempts != 0 }

func (policy RestartPolicy) MaximumAttempts() uint32       { return policy.maximumAttempts }
func (policy RestartPolicy) InitialBackoff() time.Duration { return policy.initialBackoff }
func (policy RestartPolicy) MaximumBackoff() time.Duration { return policy.maximumBackoff }
func (policy RestartPolicy) AttemptTimeout() time.Duration { return policy.attemptTimeout }

func (policy RestartPolicy) backoff(attempt uint32) time.Duration {
	if attempt <= 1 {
		return 0
	}
	value := policy.initialBackoff
	for current := uint32(2); current < attempt && value < policy.maximumBackoff; current++ {
		if value > policy.maximumBackoff/2 {
			return policy.maximumBackoff
		}
		value *= 2
	}
	return min(value, policy.maximumBackoff)
}
