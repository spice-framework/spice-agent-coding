package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/spice-framework/spice-agent/client"
)

const (
	outcomeCodeOK         = "ok"
	outcomeCodeCanceled   = "canceled"
	outcomeCodeDeadline   = "deadline"
	outcomeCodeClosed     = "closed"
	outcomeCodeCapacity   = "capacity"
	outcomeCodeRunMissing = "run_unavailable"
	outcomeCodeDependency = "dependency_unavailable"
	outcomeCodeState      = "state"
	outcomeCodeStale      = "stale_session"
	outcomeCodeUncertain  = "uncertain"
	outcomeCodeAbandon    = "abandon"
)

type runHostOutcome struct {
	Code          string `json:"code"`
	RunID         string `json:"run_id,omitempty"`
	PlanID        string `json:"plan_id,omitempty"`
	Sequence      uint64 `json:"sequence,omitempty"`
	Flag          bool   `json:"flag,omitempty"`
	AbandonCode   string `json:"abandon_code,omitempty"`
	CapacityKind  string `json:"capacity_kind,omitempty"`
	Resource      string `json:"resource,omitempty"`
	Limit         uint64 `json:"limit,omitempty"`
	Observed      uint64 `json:"observed,omitempty"`
	StaleClient   string `json:"stale_client,omitempty"`
	ExpectedEpoch uint64 `json:"expected_epoch,omitempty"`
	ObservedEpoch uint64 `json:"observed_epoch,omitempty"`
}

func successRunHostOutcome(value runHostOutcome) Outcome {
	value.Code = outcomeCodeOK
	return newRunHostOutcome(OutcomeSuccess, value)
}

func failureRunHostOutcome(err error) Outcome {
	kind := OutcomeFailure
	code := outcomeCodeState
	switch {
	case errors.Is(err, context.Canceled):
		code = outcomeCodeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		code = outcomeCodeDeadline
	case errors.Is(err, ErrRunHostClosed):
		code = outcomeCodeClosed
	case errors.Is(err, ErrRunHostCapacity):
		code = outcomeCodeCapacity
	case errors.Is(err, ErrSessionGateCapacity):
		code = outcomeCodeCapacity
	case errors.Is(err, ErrStaleSession):
		code = outcomeCodeStale
	case errors.Is(err, ErrHostedRunUnavailable):
		code = outcomeCodeRunMissing
	case errors.Is(err, ErrRunHostUncertain):
		kind = OutcomeUncertain
		code = outcomeCodeUncertain
	case errors.Is(err, ErrRunHostUnavailable):
		code = outcomeCodeDependency
	}
	value := runHostOutcome{Code: code}
	var hostCapacity *RunHostCapacityError
	var sessionCapacity *SessionGateCapacityError
	var stale *StaleSessionError
	switch {
	case errors.As(err, &hostCapacity):
		value.CapacityKind = "host"
		value.Resource = hostCapacity.Resource()
		value.Limit = hostCapacity.Limit()
		value.Observed = hostCapacity.Observed()
	case errors.As(err, &sessionCapacity):
		value.CapacityKind = "session_gate"
		value.Resource = sessionCapacity.Resource()
		value.Limit = uint64(sessionCapacity.Maximum()) // #nosec G115 -- SessionGateCapacityError has a positive bounded maximum.
		value.Observed = value.Limit + 1
	case errors.As(err, &stale):
		value.StaleClient = stale.ClientID()
		value.ExpectedEpoch = stale.ExpectedEpoch()
		value.ObservedEpoch = stale.ObservedEpoch()
	}
	return newRunHostOutcome(kind, value)
}

// abandonRunHostOutcome is consumed by doMutation before Ledger can commit it.
// Callers use it only after proving that no durable or visible boundary was
// crossed and restoring every local reservation.
func abandonRunHostOutcome(err error) Outcome {
	failed := failureRunHostOutcome(err)
	var value runHostOutcome
	if json.Unmarshal(failed.Payload(), &value) != nil {
		return failed
	}
	value.AbandonCode = value.Code
	value.Code = outcomeCodeAbandon
	return newRunHostOutcome(OutcomeFailure, value)
}

func newRunHostOutcome(kind OutcomeKind, value runHostOutcome) Outcome {
	payload, err := json.Marshal(value)
	if err != nil {
		fallback, _ := NewOutcome(OutcomeUncertain, []byte(`{"code":"uncertain"}`))
		return fallback
	}
	outcome, err := NewOutcome(kind, payload)
	if err != nil {
		fallback, _ := NewOutcome(OutcomeUncertain, []byte(`{"code":"uncertain"}`))
		return fallback
	}
	return outcome
}

func decodeRunHostOutcome(outcome Outcome) (runHostOutcome, error) {
	var value runHostOutcome
	if err := json.Unmarshal(outcome.Payload(), &value); err != nil {
		return runHostOutcome{}, ErrRunHostUnavailable
	}
	if outcome.Kind() == OutcomeUncertain || value.Code == outcomeCodeUncertain {
		return runHostOutcome{}, ErrRunHostUncertain
	}
	if outcome.Kind() == OutcomeSuccess && value.Code == outcomeCodeOK {
		return value, nil
	}
	switch value.Code {
	case outcomeCodeCanceled:
		return runHostOutcome{}, context.Canceled
	case outcomeCodeDeadline:
		return runHostOutcome{}, context.DeadlineExceeded
	case outcomeCodeClosed:
		return runHostOutcome{}, ErrRunHostClosed
	case outcomeCodeCapacity:
		return runHostOutcome{}, capacityErrorForOutcome(value)
	case outcomeCodeStale:
		if boundedToken("stale client ID", value.StaleClient) == nil && value.ExpectedEpoch != 0 &&
			value.ExpectedEpoch != value.ObservedEpoch {
			return runHostOutcome{}, staleSession(value.StaleClient, value.ExpectedEpoch, value.ObservedEpoch)
		}
		return runHostOutcome{}, ErrStaleSession
	case outcomeCodeRunMissing:
		return runHostOutcome{}, ErrHostedRunUnavailable
	case outcomeCodeDependency:
		return runHostOutcome{}, ErrRunHostUnavailable
	default:
		return runHostOutcome{}, ErrRunHostState
	}
}

func capacityErrorForOutcome(value runHostOutcome) error {
	if boundedToken("capacity resource", value.Resource) != nil || value.Limit == 0 || value.Observed <= value.Limit {
		return ErrRunHostCapacity
	}
	switch value.CapacityKind {
	case "host":
		return newRunHostCapacity(value.Resource, value.Limit, value.Observed)
	case "session_gate":
		if value.Limit > uint64(math.MaxInt) {
			return ErrRunHostCapacity
		}
		return &SessionGateCapacityError{resource: value.Resource, maximum: int(value.Limit)}
	default:
		return ErrRunHostCapacity
	}
}

func (host *RunHost) doMutation(
	ctx context.Context,
	session Session,
	operation client.OperationID,
	kind string,
	digest [32]byte,
	execute func(context.Context) Outcome,
) (runHostOutcome, bool, error) {
	if host == nil {
		return runHostOutcome{}, false, ErrRunHostClosed
	}
	if ctx == nil {
		return runHostOutcome{}, false, context.Canceled
	}
	if err := operation.Validate(); err != nil {
		return runHostOutcome{}, false, ErrRunHostState
	}
	if err := host.sessions.Check(session.ClientID(), session.Epoch()); err != nil {
		return runHostOutcome{}, false, publicRunHostError(err)
	}
	sessionContext, cancelSession := mergeContexts(ctx, session.Context())
	defer cancelSession()
	executionContext, cancelHost := mergeContexts(sessionContext, host.root)
	defer cancelHost()
	outcome, duplicate, err := host.ledger.Do(
		executionContext, session.ClientID(), operation.String(), kind, digest,
		func(operationContext context.Context) (Outcome, error) {
			if sessionErr := host.sessions.Check(session.ClientID(), session.Epoch()); sessionErr != nil {
				return Outcome{}, AbandonOperation(publicRunHostError(sessionErr))
			}
			value := execute(operationContext)
			var marker runHostOutcome
			if json.Unmarshal(value.Payload(), &marker) == nil && marker.Code == outcomeCodeAbandon {
				return Outcome{}, AbandonOperation(runHostErrorForOutcome(marker))
			}
			return value, nil
		},
	)
	if err != nil {
		if errors.Is(err, ErrOperationAbandoned) {
			return runHostOutcome{}, duplicate, publicRunHostError(err)
		}
		if errors.Is(err, ErrOperationExecutor) {
			host.degrade(degradedLifecycleCleanup)
			return runHostOutcome{}, duplicate, ErrRunHostUncertain
		}
		return runHostOutcome{}, duplicate, publicRunHostError(err)
	}
	value, decodeErr := decodeRunHostOutcome(outcome)
	if errors.Is(decodeErr, ErrRunHostUnavailable) {
		host.degrade(degradedLifecycleCleanup)
	}
	return value, duplicate, decodeErr
}

func runHostErrorForCode(code string) error {
	return runHostErrorForOutcome(runHostOutcome{AbandonCode: code})
}

func runHostErrorForOutcome(value runHostOutcome) error {
	code := value.AbandonCode
	switch code {
	case outcomeCodeCanceled:
		return context.Canceled
	case outcomeCodeDeadline:
		return context.DeadlineExceeded
	case outcomeCodeClosed:
		return ErrRunHostClosed
	case outcomeCodeCapacity:
		return capacityErrorForOutcome(value)
	case outcomeCodeStale:
		if boundedToken("stale client ID", value.StaleClient) == nil && value.ExpectedEpoch != 0 &&
			value.ExpectedEpoch != value.ObservedEpoch {
			return staleSession(value.StaleClient, value.ExpectedEpoch, value.ObservedEpoch)
		}
		return ErrStaleSession
	case outcomeCodeRunMissing:
		return ErrHostedRunUnavailable
	case outcomeCodeDependency:
		return ErrRunHostUnavailable
	case outcomeCodeUncertain:
		return ErrRunHostUncertain
	default:
		return ErrRunHostState
	}
}

func canonicalMutationDigest(value any) [32]byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return CanonicalDigest([]byte("invalid canonical mutation"))
	}
	return CanonicalDigest(encoded)
}
