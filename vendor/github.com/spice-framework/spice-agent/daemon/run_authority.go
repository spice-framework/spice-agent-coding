package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/spice-framework/spice-agent/agent"
	"github.com/spice-framework/spice-agent/daemon/internal/runauthority"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
)

var (
	// ErrRunAuthorityUnavailable reports a local storage or security failure
	// without exposing key material or platform-specific error details.
	ErrRunAuthorityUnavailable = errors.New("run authority is unavailable")
	// ErrRunAuthorityBusy reports that another process owns the stable run lock.
	ErrRunAuthorityBusy = errors.New("run authority run is already owned")
	// ErrRunAuthorityState rejects an illegal or replayed lifecycle transition.
	ErrRunAuthorityState = errors.New("run authority state transition is invalid")
	// ErrRunAuthorityVerification rejects a snapshot not authenticated by the
	// current user's persistent authority and matching suspended run record.
	ErrRunAuthorityVerification = errors.New("run authority verification failed")
	// ErrRunAuthorityUncertain reports an import consumed durably but not
	// activated. It must never be retried automatically.
	ErrRunAuthorityUncertain = errors.New("run authority import is uncertain and must not be retried")
)

// RunAuthorityConfig selects the private local authority directory. An empty
// directory uses the current user's OS configuration directory. The authority
// key is deliberately unrelated to daemon endpoint authentication tokens.
type RunAuthorityConfig struct {
	Directory string
}

// RunAuthority is an opaque per-user persistent run authority. It owns a
// random scope identity, a distinct HMAC key, signed lifecycle records, and
// stable per-run OS locks. It is suitable for constructor injection as a Spice
// singleton and never exposes its key material.
type RunAuthority struct {
	store *runauthority.Store
}

// NewRunAuthority opens or creates the current user's persistent authority.
// The owning application must Close it; generated Spice applications should
// register Close as singleton cleanup.
func NewRunAuthority(config RunAuthorityConfig) (*RunAuthority, error) {
	directory := config.Directory
	if directory != "" && !filepath.IsAbs(directory) {
		return nil, ErrRunAuthorityState
	}
	if directory == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return nil, ErrRunAuthorityUnavailable
		}
		directory = filepath.Join(root, "spice-agent", "run-authority")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, ErrRunAuthorityUnavailable
	}
	store, err := runauthority.Open(runauthority.Config{Directory: directory})
	if err != nil {
		return nil, publicAuthorityError(err)
	}
	return &RunAuthority{store: store}, nil
}

func (*RunAuthority) String() string   { return "run authority <redacted>" }
func (*RunAuthority) GoString() string { return "run authority <redacted>" }

// Close prevents new run work and releases the bound authority-directory
// handle. If a run or import lease remains open, Close returns
// ErrRunAuthorityBusy; the last lease release completes shutdown.
func (authority *RunAuthority) Close() error {
	if authority == nil || authority.store == nil {
		return nil
	}
	return publicAuthorityError(authority.store.Close())
}

// ActiveRun is an opaque exclusive lease for one active or locally suspended
// run. Typed snapshot issuance atomically persists SUSPENDED while retaining
// exclusive ownership; terminal issuance persists a tombstone before releasing
// the stable lock. The raw authority signer remains package-internal so callers
// cannot supply parallel snapshot metadata.
type ActiveRun struct {
	value *runauthority.Active
}

// Start creates a never-reused run identity at local transition generation one.
func (authority *RunAuthority) Start(ctx context.Context, runID string) (*ActiveRun, error) {
	if authority == nil || authority.store == nil {
		return nil, ErrRunAuthorityUnavailable
	}
	value, err := authority.store.Start(ctx, runID)
	if err != nil {
		return nil, publicAuthorityError(err)
	}
	return &ActiveRun{value: value}, nil
}

// RunGeneration returns the local transition generation. This value is not
// the persistent authority-key generation carried in snapshot envelopes.
func (active *ActiveRun) RunGeneration() uint64 {
	if active == nil {
		return 0
	}
	return active.value.RunGeneration()
}

// Resume invalidates the currently signed suspended snapshot, advances the
// local run generation, and retains exclusive ownership. The host must call
// Resume while the kernel remains suspended. The host first reserves the
// kernel boundary with agent.Run.PrepareLocalResume, performs this durable
// invalidation, and only then commits the prepared kernel resume. It must never
// expose or continue kernel execution before a successful authority transition.
func (active *ActiveRun) Resume(ctx context.Context) error {
	if active == nil || active.value == nil {
		return ErrRunAuthorityState
	}
	return publicAuthorityError(active.value.Resume(ctx))
}

// IssueSnapshotEnvelope validates and deterministically encodes one kernel
// snapshot, then signs it at the active run's durable lifecycle boundary. A
// successful signer result wins cancellation observed after the persistence
// boundary. Uncertain and unavailable durable outcomes are never retried or
// replaced with context cancellation.
func (active *ActiveRun) IssueSnapshotEnvelope(
	ctx context.Context,
	snapshot agent.Snapshot,
) (*enginev1.SnapshotEnvelope, error) {
	if active == nil || active.value == nil {
		return nil, ErrRunAuthorityState
	}
	return issueSnapshotEnvelope(ctx, snapshot, active.value)
}

type snapshotEnvelopeAuthority interface {
	enginev1.SnapshotAuthoritySigner
	SnapshotIssuePreflight(string) error
	SnapshotIssueError() error
}

func issueSnapshotEnvelope(
	ctx context.Context,
	snapshot agent.Snapshot,
	authority snapshotEnvelopeAuthority,
) (*enginev1.SnapshotEnvelope, error) {
	if authority == nil {
		return nil, ErrRunAuthorityState
	}
	if err := snapshot.Validate(); err != nil {
		return nil, ErrRunAuthorityState
	}
	switch preflightErr := authority.SnapshotIssuePreflight(snapshot.RunID()); {
	case errors.Is(preflightErr, runauthority.ErrUncertain):
		return nil, ErrRunAuthorityUncertain
	case errors.Is(preflightErr, runauthority.ErrUnavailable):
		return nil, ErrRunAuthorityUnavailable
	case preflightErr != nil:
		return nil, ErrRunAuthorityState
	}
	lifecycle, ok := snapshotLifecycle(snapshot.Status())
	if !ok {
		return nil, ErrRunAuthorityState
	}
	payload, err := snapshot.MarshalBinary()
	if err != nil {
		return nil, ErrRunAuthorityState
	}
	envelope, err := enginev1.NewSnapshotEnvelope(
		ctx,
		authority,
		snapshot.RunID(),
		snapshot.LastSequence(),
		lifecycle,
		payload,
	)
	if err == nil {
		return envelope, nil
	}
	switch issueErr := authority.SnapshotIssueError(); {
	case errors.Is(issueErr, runauthority.ErrUncertain):
		return nil, ErrRunAuthorityUncertain
	case errors.Is(issueErr, runauthority.ErrUnavailable):
		return nil, ErrRunAuthorityUnavailable
	}
	switch postflightErr := authority.SnapshotIssuePreflight(snapshot.RunID()); {
	case errors.Is(postflightErr, runauthority.ErrUncertain):
		return nil, ErrRunAuthorityUncertain
	case errors.Is(postflightErr, runauthority.ErrUnavailable):
		return nil, ErrRunAuthorityUnavailable
	case postflightErr != nil:
		return nil, ErrRunAuthorityState
	}
	if cause := authorityContextCause(ctx); cause != nil && errors.Is(err, cause) {
		return nil, cause
	}
	return nil, ErrRunAuthorityState
}

func snapshotLifecycle(status agent.LifecycleStatus) (enginev1.SnapshotLifecycle, bool) {
	switch status {
	case agent.LifecycleSuspended:
		return enginev1.SnapshotLifecycle_SNAPSHOT_LIFECYCLE_SUSPENDED, true
	case agent.LifecycleCompleted:
		return enginev1.SnapshotLifecycle_SNAPSHOT_LIFECYCLE_COMPLETED, true
	case agent.LifecycleFailed:
		return enginev1.SnapshotLifecycle_SNAPSHOT_LIFECYCLE_FAILED, true
	case agent.LifecycleCancelled:
		return enginev1.SnapshotLifecycle_SNAPSHOT_LIFECYCLE_CANCELLED, true
	default:
		return enginev1.SnapshotLifecycle_SNAPSHOT_LIFECYCLE_UNSPECIFIED, false
	}
}

func authorityContextCause(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return context.Cause(ctx)
}

// TerminalPhase is a durable non-resumable run tombstone.
type TerminalPhase string

const (
	TerminalCompleted TerminalPhase = "COMPLETED"
	TerminalFailed    TerminalPhase = "FAILED"
	TerminalCancelled TerminalPhase = "CANCELLED"
)

// Terminal persists a tombstone before releasing ownership.
func (active *ActiveRun) Terminal(ctx context.Context, phase TerminalPhase) error {
	if active == nil || active.value == nil {
		return ErrRunAuthorityState
	}
	var internal runauthority.Phase
	switch phase {
	case TerminalCompleted:
		internal = runauthority.PhaseCompleted
	case TerminalFailed:
		internal = runauthority.PhaseFailed
	case TerminalCancelled:
		internal = runauthority.PhaseCancelled
	default:
		return ErrRunAuthorityState
	}
	return publicAuthorityError(active.value.Terminal(ctx, internal))
}

// Close releases the OS lock without changing durable state. It is the crash
// equivalent: ACTIVE remains non-importable, while a valid SUSPENDED record
// becomes eligible for another authority instance to import. An uncertain
// owner is still released, but its first Close reports
// ErrRunAuthorityUncertain; repeated Close is idempotent.
func (active *ActiveRun) Close() error {
	if active == nil || active.value == nil {
		return nil
	}
	return publicAuthorityError(active.value.Close())
}

// RunImport is an opaque prepared import transaction and authenticated
// snapshot verifier. It owns the stable run lock until Abort or Activate.
type RunImport struct {
	value *runauthority.Import
}

// PrepareImport acquires the stable lock and verifies both the keyed envelope
// and its exact signed SUSPENDED record. No durable state changes yet.
func (authority *RunAuthority) PrepareImport(
	ctx context.Context,
	snapshot *enginev1.SnapshotEnvelope,
) (*RunImport, error) {
	if authority == nil || authority.store == nil {
		return nil, ErrRunAuthorityUnavailable
	}
	value, err := authority.store.PrepareImport(ctx, snapshot)
	if err != nil {
		return nil, publicAuthorityError(err)
	}
	return &RunImport{value: value}, nil
}

// VerifySnapshot implements enginev1.SnapshotAuthorityVerifier and remains
// bound to the exact snapshot claim used to prepare this transaction.
func (transaction *RunImport) VerifySnapshot(
	ctx context.Context,
	input enginev1.SnapshotAuthorityInput,
	claim *enginev1.SnapshotAuthority,
) error {
	if transaction == nil || transaction.value == nil {
		return enginev1.ErrSnapshotAuthorityVerification
	}
	err := transaction.value.VerifySnapshot(ctx, input, claim)
	if errors.Is(err, runauthority.ErrUncertain) {
		return ErrRunAuthorityUncertain
	}
	return err
}

// Consume durably advances to IMPORTING. After success, failure is uncertain
// and the same snapshot must never be retried automatically.
func (transaction *RunImport) Consume(ctx context.Context) error {
	if transaction == nil || transaction.value == nil {
		return ErrRunAuthorityState
	}
	return publicAuthorityError(transaction.value.Consume(ctx))
}

// Activate durably advances to ACTIVE and transfers the held lock to the
// returned run lease. Call it only after prepared kernel execution commits. If
// Activate returns ErrRunAuthorityUncertain, the transaction still owns the
// lock: stop and join the committed kernel run before Abort or Close releases
// it. Never continue that run without a returned ActiveRun lease.
func (transaction *RunImport) Activate(ctx context.Context) (*ActiveRun, error) {
	if transaction == nil || transaction.value == nil {
		return nil, ErrRunAuthorityState
	}
	value, err := transaction.value.Activate(ctx)
	if err != nil {
		return nil, publicAuthorityError(err)
	}
	return &ActiveRun{value: value}, nil
}

// Abort releases a pre-consume transaction. After Consume it returns
// ErrRunAuthorityUncertain while still releasing the process lock.
func (transaction *RunImport) Abort() error {
	if transaction == nil || transaction.value == nil {
		return nil
	}
	return publicAuthorityError(transaction.value.Abort())
}

// Close is equivalent to Abort.
func (transaction *RunImport) Close() error { return transaction.Abort() }

func publicAuthorityError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, runauthority.ErrBusy):
		return ErrRunAuthorityBusy
	case errors.Is(err, runauthority.ErrVerification):
		return ErrRunAuthorityVerification
	case errors.Is(err, runauthority.ErrUncertain):
		return ErrRunAuthorityUncertain
	case errors.Is(err, runauthority.ErrState), errors.Is(err, runauthority.ErrClosed), errors.Is(err, runauthority.ErrConfiguration):
		return ErrRunAuthorityState
	default:
		return ErrRunAuthorityUnavailable
	}
}

var _ enginev1.SnapshotAuthorityVerifier = (*RunImport)(nil)
