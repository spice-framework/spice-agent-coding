// Package retry provides explicit, bounded, context-aware retry execution.
package retry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrPanicked identifies an observed operation panic. Do reports the attempt
// and re-panics with the original value.
var ErrPanicked = errors.New("retry operation panicked")

// Attempt identifies one one-based invocation within a bounded policy.
type Attempt struct {
	Number int
	Max    int
}

// Retryable decides whether an operation error is safe to retry.
type Retryable func(error) bool

// TransientError explicitly marks whether an error is safe to retry. Generated
// policies use this narrow contract when no application classifier is named.
type TransientError interface {
	error
	Transient() bool
}

// Transient retries only errors that explicitly implement TransientError and
// return true. Cancellation and deadline errors are never retryable.
func Transient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	transient, ok := errors.AsType[TransientError](err)
	return ok && transient.Transient()
}

// Jitter explicitly adjusts one computed backoff. It must return a duration
// between zero and Policy.MaxBackoff.
type Jitter func(Attempt, time.Duration) time.Duration

// Waiter waits between attempts. A nil Waiter uses a context-aware timer.
// Explicit waiters support virtual clocks and application-specific scheduling.
type Waiter func(context.Context, time.Duration) error

// Observation is one completed attempt. NextBackoff is non-zero only when
// another attempt will be made.
type Observation struct {
	ID          string
	Module      string
	Attempt     Attempt
	Duration    time.Duration
	Err         error
	NextBackoff time.Duration
	Panicked    bool
}

// Observer receives completed attempts synchronously on the executing
// goroutine. It must not panic or block indefinitely.
type Observer func(context.Context, Observation)

// Policy is one immutable retry execution policy. More than one attempt
// requires an explicit Retryable classifier.
type Policy struct {
	ID             string
	Module         string
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Multiplier     uint32
	Retryable      Retryable
	Jitter         Jitter
	Wait           Waiter
	Observer       Observer
}

// ExhaustedError reports that every permitted attempt returned a retryable
// error.
type ExhaustedError struct {
	Attempts int
	Last     error
}

// Error describes the exhausted policy.
func (err *ExhaustedError) Error() string {
	if err == nil {
		return "retry attempts exhausted"
	}
	return fmt.Sprintf("retry attempts exhausted after %d attempts: %v", err.Attempts, err.Last)
}

// Unwrap exposes the final attempt error.
func (err *ExhaustedError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Last
}

// Do executes an operation until it succeeds, returns a non-retryable error,
// is canceled, or exhausts the policy.
func Do[T any](
	ctx context.Context,
	policy Policy,
	operation func(context.Context, Attempt) (T, error),
) (result T, resultErr error) {
	if ctx == nil {
		return result, errors.New("execute retry: context is nil")
	}
	if err := validatePolicy(policy); err != nil {
		return result, err
	}
	if operation == nil {
		return result, fmt.Errorf("execute retry %q: operation is nil", policy.ID)
	}
	multiplier := policy.Multiplier
	if multiplier == 0 {
		multiplier = 2
	}
	backoff := policy.InitialBackoff

	for number := 1; number <= policy.MaxAttempts; number++ {
		if cause := context.Cause(ctx); cause != nil {
			return result, fmt.Errorf("execute retry %q: %w", policy.ID, cause)
		}
		attempt := Attempt{Number: number, Max: policy.MaxAttempts}
		started := time.Now()
		value, panicked, err := invoke(ctx, attempt, operation)
		if panicked != nil {
			observe(policy, ctx, Observation{
				ID:       policy.ID,
				Module:   policy.Module,
				Attempt:  attempt,
				Duration: time.Since(started),
				Err:      ErrPanicked,
				Panicked: true,
			})
			panic(panicked)
		}
		if err == nil {
			observe(policy, ctx, Observation{
				ID:       policy.ID,
				Module:   policy.Module,
				Attempt:  attempt,
				Duration: time.Since(started),
			})
			return value, nil
		}

		attemptErr := fmt.Errorf("execute retry %q attempt %d: %w", policy.ID, number, err)
		if cause := context.Cause(ctx); cause != nil {
			resultErr = errors.Join(attemptErr, fmt.Errorf("retry canceled: %w", cause))
			observeFailure(policy, ctx, attempt, started, resultErr, 0)
			return result, resultErr
		}
		if !policy.Retryable(err) {
			observeFailure(policy, ctx, attempt, started, attemptErr, 0)
			return result, attemptErr
		}
		if number == policy.MaxAttempts {
			resultErr = &ExhaustedError{Attempts: number, Last: attemptErr}
			observeFailure(policy, ctx, attempt, started, resultErr, 0)
			return result, resultErr
		}

		delay, delayErr := jitteredBackoff(policy, attempt, backoff)
		if delayErr != nil {
			resultErr = errors.Join(attemptErr, delayErr)
			observeFailure(policy, ctx, attempt, started, resultErr, 0)
			return result, resultErr
		}
		observeFailure(policy, ctx, attempt, started, attemptErr, delay)
		if err := wait(ctx, policy.Wait, delay); err != nil {
			return result, errors.Join(attemptErr, fmt.Errorf("wait after retry attempt %d: %w", number, err))
		}
		backoff = nextBackoff(backoff, policy.MaxBackoff, multiplier)
	}
	panic("unreachable retry loop")
}

// Run is the error-only form of Do.
func Run(
	ctx context.Context,
	policy Policy,
	operation func(context.Context, Attempt) error,
) error {
	if operation == nil {
		return fmt.Errorf("execute retry %q: operation is nil", policy.ID)
	}
	_, err := Do(ctx, policy, func(ctx context.Context, attempt Attempt) (struct{}, error) {
		return struct{}{}, operation(ctx, attempt)
	})
	return err
}

func validatePolicy(policy Policy) error {
	if policy.ID == "" {
		return errors.New("execute retry: policy ID is required")
	}
	if policy.Module == "" {
		return fmt.Errorf("execute retry %q: module is required", policy.ID)
	}
	if policy.MaxAttempts < 1 {
		return fmt.Errorf("execute retry %q: max attempts must be positive", policy.ID)
	}
	if policy.MaxAttempts > 1 && policy.Retryable == nil {
		return fmt.Errorf(
			"execute retry %q: retry classifier is required for multiple attempts",
			policy.ID,
		)
	}
	if policy.InitialBackoff < 0 {
		return fmt.Errorf("execute retry %q: initial backoff must not be negative", policy.ID)
	}
	if policy.MaxBackoff < policy.InitialBackoff {
		return fmt.Errorf(
			"execute retry %q: max backoff must be at least initial backoff",
			policy.ID,
		)
	}
	if policy.Multiplier == 1 {
		return fmt.Errorf("execute retry %q: multiplier must be zero or at least two", policy.ID)
	}
	return nil
}

func invoke[T any](
	ctx context.Context,
	attempt Attempt,
	operation func(context.Context, Attempt) (T, error),
) (value T, panicked any, err error) {
	defer func() {
		panicked = recover()
	}()
	value, err = operation(ctx, attempt)
	return value, nil, err
}

func jitteredBackoff(policy Policy, attempt Attempt, base time.Duration) (time.Duration, error) {
	if policy.Jitter == nil {
		return base, nil
	}
	delay := policy.Jitter(attempt, base)
	if delay < 0 || delay > policy.MaxBackoff {
		return 0, fmt.Errorf(
			"execute retry %q: jitter returned backoff %s outside [0s, %s]",
			policy.ID,
			delay,
			policy.MaxBackoff,
		)
	}
	return delay, nil
}

func nextBackoff(current, maximum time.Duration, multiplier uint32) time.Duration {
	if current == 0 || current == maximum {
		return current
	}
	if current > maximum/time.Duration(multiplier) {
		return maximum
	}
	next := current * time.Duration(multiplier)
	if next > maximum {
		return maximum
	}
	return next
}

func wait(ctx context.Context, waiter Waiter, delay time.Duration) error {
	if waiter != nil {
		return waiter(ctx, delay)
	}
	if delay == 0 {
		return context.Cause(ctx)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func observeFailure(
	policy Policy,
	ctx context.Context,
	attempt Attempt,
	started time.Time,
	err error,
	next time.Duration,
) {
	observe(policy, ctx, Observation{
		ID:          policy.ID,
		Module:      policy.Module,
		Attempt:     attempt,
		Duration:    time.Since(started),
		Err:         err,
		NextBackoff: next,
	})
}

func observe(policy Policy, ctx context.Context, observation Observation) {
	if policy.Observer != nil {
		policy.Observer(ctx, observation)
	}
}
