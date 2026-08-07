package runauthority

import (
	"context"
	"encoding/hex"
	"math"
	"slices"
	"sync"

	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
)

type Active struct {
	mu               sync.Mutex
	store            *Store
	lock             *stableLock
	runID            string
	runGeneration    uint64
	suspended        *snapshotRecord
	uncertain        bool
	snapshotIssueErr error
	closed           bool
}

func (active *Active) RunGeneration() uint64 {
	if active == nil {
		return 0
	}
	active.mu.Lock()
	defer active.mu.Unlock()
	return active.runGeneration
}

// SnapshotIssueError classifies the last failed snapshot issuance without
// exposing platform, key, or signer details. It is safe to call after a
// concurrent signing attempt completes.
func (active *Active) SnapshotIssueError() error {
	if active == nil {
		return ErrState
	}
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.uncertain {
		return ErrUncertain
	}
	if active.snapshotIssueErr != nil {
		return active.snapshotIssueErr
	}
	return ErrState
}

// SnapshotIssuePreflight rejects an invalid run identity or a lease that is
// already unable to issue before a caller's context can mask that state.
func (active *Active) SnapshotIssuePreflight(runID string) error {
	if active == nil {
		return ErrState
	}
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.uncertain {
		return ErrUncertain
	}
	if active.snapshotIssueErr != nil {
		return active.snapshotIssueErr
	}
	if active.closed || active.lock == nil || runID != active.runID {
		return ErrState
	}
	return nil
}

func (active *Active) SignSnapshot(
	ctx context.Context,
	input enginev1.SnapshotAuthorityInput,
) (*enginev1.SnapshotAuthority, error) {
	if active == nil {
		return nil, enginev1.ErrSnapshotAuthoritySigning
	}
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.closed || active.lock == nil || active.uncertain || input.RunID() != active.runID {
		return nil, enginev1.ErrSnapshotAuthoritySigning
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	phase, resumable := phaseForLifecycle(input.Lifecycle())
	if phase == "" {
		return nil, enginev1.ErrSnapshotAuthoritySigning
	}
	if resumable && active.runGeneration == math.MaxUint64 {
		return nil, enginev1.ErrSnapshotAuthoritySigning
	}
	codec, err := active.store.snapshotCodec(active.runID, active.runGeneration)
	if err != nil {
		return nil, enginev1.ErrSnapshotAuthoritySigning
	}
	claim, err := codec.SignSnapshot(ctx, input)
	if err != nil {
		return nil, err
	}
	value := active.store.baseRecord(active.runID, active.runGeneration, phase)
	if resumable {
		value.Snapshot = &snapshotRecord{
			Format: input.Format(), LastSequence: input.LastSequence(), Lifecycle: int32(input.Lifecycle()),
			PayloadSHA: hex.EncodeToString(input.PayloadSHA256()), AuthorityMAC: hex.EncodeToString(claim.GetHmacSha256()),
		}
		if active.suspended != nil {
			if *active.suspended != *value.Snapshot {
				return nil, enginev1.ErrSnapshotAuthoritySigning
			}
			return cloneAuthority(claim), nil
		}
	}
	attempted, err := active.store.writeRecord(ctx, active.runID, value)
	if err != nil {
		if attempted {
			active.uncertain = true
			active.snapshotIssueErr = ErrUncertain
			active.store.markUncertain(active.runID)
			return nil, enginev1.ErrSnapshotAuthoritySigning
		}
		return nil, err
	}
	if resumable {
		snapshotCopy := *value.Snapshot
		active.suspended = &snapshotCopy
		return cloneAuthority(claim), nil
	}
	if err = active.finish(); err != nil {
		return nil, enginev1.ErrSnapshotAuthoritySigning
	}
	return cloneAuthority(claim), nil
}

func (active *Active) Resume(ctx context.Context) error {
	if active == nil {
		return ErrClosed
	}
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.uncertain {
		return ErrUncertain
	}
	if active.closed || active.lock == nil {
		return ErrClosed
	}
	if active.suspended == nil {
		return ErrState
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if active.runGeneration == math.MaxUint64 {
		return ErrState
	}
	nextGeneration := active.runGeneration + 1
	value := active.store.baseRecord(active.runID, nextGeneration, PhaseActive)
	attempted, err := active.store.writeRecord(ctx, active.runID, value)
	if err != nil {
		if attempted {
			active.uncertain = true
			active.store.markUncertain(active.runID)
			return ErrUncertain
		}
		return err
	}
	active.runGeneration = nextGeneration
	active.suspended = nil
	return nil
}

func (active *Active) Terminal(ctx context.Context, phase Phase) error {
	if active == nil {
		return ErrClosed
	}
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.uncertain {
		return ErrUncertain
	}
	if active.closed || active.lock == nil {
		return ErrClosed
	}
	if phase != PhaseCompleted && phase != PhaseFailed && phase != PhaseCancelled {
		return ErrState
	}
	value := active.store.baseRecord(active.runID, active.runGeneration, phase)
	attempted, err := active.store.writeRecord(ctx, active.runID, value)
	if err != nil {
		if attempted {
			active.uncertain = true
			active.store.markUncertain(active.runID)
			return ErrUncertain
		}
		return err
	}
	return active.finish()
}

func (active *Active) Close() error {
	if active == nil {
		return nil
	}
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.closed {
		return nil
	}
	uncertain := active.uncertain
	finishErr := active.finish()
	if uncertain {
		return ErrUncertain
	}
	return finishErr
}

func (active *Active) finish() error {
	if active.closed || active.lock == nil {
		return nil
	}
	lockErr := active.lock.close()
	active.lock = nil
	active.closed = true
	leaseErr := active.store.endLease()
	if lockErr != nil || leaseErr != nil {
		if !active.uncertain {
			active.snapshotIssueErr = ErrUnavailable
		}
		return ErrUnavailable
	}
	return nil
}

type Import struct {
	mu                  sync.Mutex
	store               *Store
	lock                *stableLock
	runID               string
	sourceRunGeneration uint64
	expectedScope       []byte
	expectedMAC         []byte
	codec               *enginev1.HMACSnapshotAuthority
	consumed            bool
	uncertain           bool
	closed              bool
}

func (transaction *Import) VerifySnapshot(
	ctx context.Context,
	input enginev1.SnapshotAuthorityInput,
	claim *enginev1.SnapshotAuthority,
) error {
	if transaction == nil {
		return enginev1.ErrSnapshotAuthorityVerification
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.uncertain {
		return ErrUncertain
	}
	if transaction.closed || transaction.consumed || input.RunID() != transaction.runID || claim == nil ||
		!slices.Equal(claim.GetScopeId(), transaction.expectedScope) ||
		!slices.Equal(claim.GetHmacSha256(), transaction.expectedMAC) ||
		claim.GetGeneration() != transaction.store.authorityGeneration {
		return enginev1.ErrSnapshotAuthorityVerification
	}
	return transaction.codec.VerifySnapshot(ctx, input, claim)
}

func (transaction *Import) Consume(ctx context.Context) error {
	if transaction == nil {
		return ErrClosed
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.uncertain {
		return ErrUncertain
	}
	if transaction.closed || transaction.consumed {
		return ErrState
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if transaction.sourceRunGeneration == math.MaxUint64 {
		return ErrState
	}
	// Crossing into the write attempt is the point of no safe return: atomic
	// replacement or its final durability barrier may have succeeded even when
	// the platform reports an error.
	value := transaction.store.baseRecord(transaction.runID, transaction.sourceRunGeneration+1, PhaseImporting)
	attempted, err := transaction.store.writeRecord(ctx, transaction.runID, value)
	if err != nil {
		if !attempted {
			return err
		}
		transaction.consumed = true
		transaction.uncertain = true
		transaction.store.markUncertain(transaction.runID)
		return ErrUncertain
	}
	transaction.consumed = true
	return nil
}

func (transaction *Import) Activate(ctx context.Context) (*Active, error) {
	if transaction == nil {
		return nil, ErrClosed
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.uncertain {
		return nil, ErrUncertain
	}
	if transaction.closed || !transaction.consumed || transaction.lock == nil {
		return nil, ErrState
	}
	runGeneration := transaction.sourceRunGeneration + 1
	value := transaction.store.baseRecord(transaction.runID, runGeneration, PhaseActive)
	attempted, err := transaction.store.writeRecord(ctx, transaction.runID, value)
	if err != nil {
		if !attempted {
			return nil, err
		}
		transaction.uncertain = true
		transaction.store.markUncertain(transaction.runID)
		return nil, ErrUncertain
	}
	active := &Active{
		store: transaction.store, lock: transaction.lock, runID: transaction.runID, runGeneration: runGeneration,
	}
	transaction.lock = nil
	transaction.closed = true
	return active, nil
}

func (transaction *Import) Abort() error {
	if transaction == nil {
		return nil
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.closed {
		return nil
	}
	lockErr := transaction.lock.close()
	transaction.lock = nil
	transaction.closed = true
	leaseErr := transaction.store.endLease()
	if transaction.consumed || transaction.uncertain {
		return ErrUncertain
	}
	if lockErr != nil || leaseErr != nil {
		return ErrUnavailable
	}
	return nil
}

func (transaction *Import) Close() error { return transaction.Abort() }

func phaseForLifecycle(lifecycle enginev1.SnapshotLifecycle) (Phase, bool) {
	switch lifecycle {
	case enginev1.SnapshotLifecycle_SNAPSHOT_LIFECYCLE_SUSPENDED:
		return PhaseSuspended, true
	case enginev1.SnapshotLifecycle_SNAPSHOT_LIFECYCLE_COMPLETED:
		return PhaseCompleted, false
	case enginev1.SnapshotLifecycle_SNAPSHOT_LIFECYCLE_FAILED:
		return PhaseFailed, false
	case enginev1.SnapshotLifecycle_SNAPSHOT_LIFECYCLE_CANCELLED:
		return PhaseCancelled, false
	default:
		return "", false
	}
}

func cloneAuthority(value *enginev1.SnapshotAuthority) *enginev1.SnapshotAuthority {
	if value == nil {
		return nil
	}
	return &enginev1.SnapshotAuthority{
		ScopeId: slices.Clone(value.GetScopeId()), Generation: value.GetGeneration(),
		HmacSha256: slices.Clone(value.GetHmacSha256()),
	}
}

var (
	_ enginev1.SnapshotAuthoritySigner   = (*Active)(nil)
	_ enginev1.SnapshotAuthorityVerifier = (*Import)(nil)
)
