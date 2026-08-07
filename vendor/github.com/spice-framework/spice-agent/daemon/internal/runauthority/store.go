// Package runauthority owns the crash-safe, per-user authority for daemon runs.
package runauthority

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"

	commonv1 "github.com/spice-framework/spice-agent/common/v1"
	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
)

const (
	identityBytes     = 8 + sha256.Size + sha256.Size
	maximumRecordSize = 16 << 10
	recordVersion     = 1
	recordDomain      = "spice.agent.run-authority-record/v1"
	runKeyDomain      = "spice.agent.run-authority-run-key/v1"
)

var (
	ErrConfiguration = errors.New("run authority configuration is invalid")
	ErrUnavailable   = errors.New("run authority is unavailable")
	ErrBusy          = errors.New("run authority run is already owned")
	ErrState         = errors.New("run authority state transition is invalid")
	ErrVerification  = errors.New("run authority verification failed")
	ErrUncertain     = errors.New("run authority import is uncertain and must not be retried")
	ErrClosed        = errors.New("run authority transaction is closed")
)

type Phase string

const (
	PhaseActive    Phase = "ACTIVE"
	PhaseSuspended Phase = "SUSPENDED"
	PhaseImporting Phase = "IMPORTING"
	PhaseCompleted Phase = "COMPLETED"
	PhaseFailed    Phase = "FAILED"
	PhaseCancelled Phase = "CANCELLED"
)

type Config struct {
	Directory string
	Random    io.Reader
	writeFile func(string, []byte) error
}

type Store struct {
	mu                  sync.Mutex
	directory           *secureDirectory
	directoryPath       string
	authorityGeneration uint64
	scopeID             [sha256.Size]byte
	key                 [sha256.Size]byte
	writeFile           func(string, []byte) error
	uncertain           map[string]struct{}
	leases              int
	closing             bool
	closed              bool
	closeDone           chan struct{}
	closeErr            error
}

type snapshotRecord struct {
	Format       string `json:"format"`
	LastSequence uint64 `json:"last_sequence"`
	Lifecycle    int32  `json:"lifecycle"`
	PayloadSHA   string `json:"payload_sha256"`
	AuthorityMAC string `json:"authority_hmac_sha256"`
}

type record struct {
	Version             int             `json:"version"`
	ScopeID             string          `json:"scope_id"`
	AuthorityGeneration uint64          `json:"authority_key_generation"`
	RunID               string          `json:"run_id"`
	RunGeneration       uint64          `json:"run_generation"`
	Phase               Phase           `json:"phase"`
	Snapshot            *snapshotRecord `json:"snapshot,omitempty"`
}

type signedRecord struct {
	Record record `json:"record"`
	MAC    string `json:"hmac_sha256"`
}

func Open(config Config) (*Store, error) {
	if config.Directory == "" || config.Directory != filepath.Clean(config.Directory) {
		return nil, ErrConfiguration
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	absolute, err := filepath.Abs(config.Directory)
	if err != nil || absolute != config.Directory {
		return nil, ErrConfiguration
	}
	directory, err := bindSecureDirectory(absolute)
	if err != nil {
		return nil, ErrUnavailable
	}
	keepDirectory := false
	defer func() {
		if !keepDirectory {
			_ = directory.close()
		}
	}()
	if config.writeFile == nil {
		config.writeFile = directory.writeFileAtomic
	}
	identity, err := loadOrCreateIdentity(directory, config.Random, config.writeFile)
	if err != nil {
		return nil, err
	}
	defer clear(identity)
	result := &Store{
		directory: directory, directoryPath: absolute,
		authorityGeneration: binary.BigEndian.Uint64(identity[:8]), writeFile: config.writeFile,
		uncertain: make(map[string]struct{}), closeDone: make(chan struct{}),
	}
	if result.authorityGeneration == 0 {
		return nil, ErrUnavailable
	}
	copy(result.scopeID[:], identity[8:8+sha256.Size])
	copy(result.key[:], identity[8+sha256.Size:])
	if allZero(result.scopeID[:]) || allZero(result.key[:]) || bytes.Equal(result.scopeID[:], result.key[:]) {
		return nil, ErrUnavailable
	}
	keepDirectory = true
	return result, nil
}

func loadOrCreateIdentity(
	directory *secureDirectory,
	random io.Reader,
	writeFile func(string, []byte) error,
) ([]byte, error) {
	identityLock, err := directory.acquireInitializationLock("identity.lock")
	if err != nil {
		return nil, classifyLockError(err)
	}
	defer func() { _ = identityLock.close() }()
	identity, err := directory.readFile("identity.key", identityBytes)
	keepIdentity := false
	defer func() {
		if !keepIdentity {
			clear(identity)
		}
	}()
	if errors.Is(err, os.ErrNotExist) {
		identity = make([]byte, identityBytes)
		binary.BigEndian.PutUint64(identity[:8], 1)
		if _, err = io.ReadFull(random, identity[8:]); err != nil {
			return nil, ErrUnavailable
		}
		if bytes.Equal(identity[8:8+sha256.Size], identity[8+sha256.Size:]) ||
			writeFile("identity.key", identity) != nil {
			return nil, ErrUnavailable
		}
	} else if err != nil || len(identity) != identityBytes {
		return nil, ErrUnavailable
	}
	if err = identityLock.close(); err != nil {
		return nil, ErrUnavailable
	}
	keepIdentity = true
	return identity, nil
}

func (*Store) String() string   { return "run authority <redacted>" }
func (*Store) GoString() string { return "run authority <redacted>" }

func (store *Store) Close() error {
	if store == nil || store.directory == nil {
		return nil
	}
	store.mu.Lock()
	if store.closed {
		done := store.closeDone
		store.mu.Unlock()
		<-done
		return store.closeErr
	}
	store.closing = true
	if store.leases != 0 {
		store.mu.Unlock()
		return ErrBusy
	}
	store.closed = true
	store.mu.Unlock()
	return store.finishDirectoryClose()
}

func (store *Store) Start(ctx context.Context, runID string) (*Active, error) {
	if store == nil {
		return nil, ErrUnavailable
	}
	if err := validateContextAndRun(ctx, runID); err != nil {
		return nil, err
	}
	if err := store.beginLease(); err != nil {
		return nil, err
	}
	releaseLease := true
	defer func() {
		if releaseLease {
			_ = store.endLease()
		}
	}()
	if store.isUncertain(runID) {
		return nil, ErrUncertain
	}
	lock, err := store.directory.acquireStableLock(store.lockName(runID))
	if err != nil {
		return nil, classifyLockError(err)
	}
	if _, err = store.readRecord(runID); err == nil {
		_ = lock.close()
		return nil, ErrState
	}
	if !errors.Is(err, os.ErrNotExist) {
		_ = lock.close()
		if errors.Is(err, ErrVerification) {
			return nil, ErrVerification
		}
		return nil, ErrUnavailable
	}
	value := store.baseRecord(runID, 1, PhaseActive)
	attempted, err := store.writeRecord(ctx, runID, value)
	if err != nil {
		_ = lock.close()
		if attempted {
			store.markUncertain(runID)
			return nil, ErrUncertain
		}
		return nil, err
	}
	releaseLease = false
	return &Active{store: store, lock: lock, runID: runID, runGeneration: 1}, nil
}

func (store *Store) PrepareImport(ctx context.Context, snapshot *enginev1.SnapshotEnvelope) (*Import, error) {
	if store == nil || ctx == nil || snapshot == nil {
		return nil, ErrVerification
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if err := enginev1.ValidateSnapshotEnvelope(snapshot); err != nil ||
		snapshot.GetLifecycle() != enginev1.SnapshotLifecycle_SNAPSHOT_LIFECYCLE_SUSPENDED {
		return nil, ErrVerification
	}
	if err := store.beginLease(); err != nil {
		return nil, err
	}
	releaseLease := true
	defer func() {
		if releaseLease {
			_ = store.endLease()
		}
	}()
	runID := snapshot.GetRunId()
	if store.isUncertain(runID) {
		return nil, ErrUncertain
	}
	lock, err := store.directory.acquireStableLock(store.lockName(runID))
	if err != nil {
		return nil, classifyLockError(err)
	}
	value, err := store.readRecord(runID)
	if err != nil || !store.matchesSuspended(value, snapshot) {
		_ = lock.close()
		return nil, ErrVerification
	}
	codec, err := store.snapshotCodec(runID, value.RunGeneration)
	if err != nil {
		_ = lock.close()
		return nil, ErrVerification
	}
	request := &enginev1.ImportSnapshotRequest{
		ClientId: "run-authority", OwnershipEpoch: 1, ClientOperationId: "prepare-import", Snapshot: snapshot,
	}
	if err = enginev1.ValidateImportSnapshotRequest(ctx, request, codec, snapshotImportLimits()); err != nil {
		_ = lock.close()
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		return nil, ErrVerification
	}
	releaseLease = false
	return &Import{
		store: store, lock: lock, runID: runID, sourceRunGeneration: value.RunGeneration,
		expectedScope: slices.Clone(snapshot.GetAuthority().GetScopeId()),
		expectedMAC:   slices.Clone(snapshot.GetAuthority().GetHmacSha256()), codec: codec,
	}, nil
}

func snapshotImportLimits() *commonv1.Limits {
	return &commonv1.Limits{
		MaxMessageBytes:    uint64(enginev1.MaximumSnapshotEnvelopeBytes + 1024),
		MaxCollectionItems: 1, MaxReplayEvents: 1, MaxReplayBytes: 1,
		MaxConcurrentStreams: 1, MaxActiveRuns: 1,
	}
}

func (store *Store) baseRecord(runID string, runGeneration uint64, phase Phase) record {
	return record{
		Version: recordVersion, ScopeID: hex.EncodeToString(store.scopeID[:]),
		AuthorityGeneration: store.authorityGeneration, RunID: runID,
		RunGeneration: runGeneration, Phase: phase,
	}
}

func (store *Store) snapshotCodec(runID string, runGeneration uint64) (*enginev1.HMACSnapshotAuthority, error) {
	if runGeneration == 0 {
		return nil, ErrState
	}
	mac := hmac.New(sha256.New, store.key[:])
	writeMACPart(mac, []byte(runKeyDomain))
	writeMACPart(mac, store.scopeID[:])
	writeMACPart(mac, []byte(runID))
	var generation [8]byte
	binary.BigEndian.PutUint64(generation[:], runGeneration)
	writeMACPart(mac, generation[:])
	derived := mac.Sum(nil)
	defer clear(derived)
	return enginev1.NewHMACSnapshotAuthority(store.scopeID[:], store.authorityGeneration, derived)
}

func (store *Store) statePath(runID string) string {
	return filepath.Join(store.directoryPath, store.stateName(runID))
}

func (*Store) lockName(runID string) string  { return runName(runID) + ".lock" }
func (*Store) stateName(runID string) string { return runName(runID) + ".state" }

func runName(runID string) string {
	digest := sha256.Sum256([]byte(runID))
	return "run-" + hex.EncodeToString(digest[:])
}

func (store *Store) writeRecord(ctx context.Context, runID string, value record) (bool, error) {
	if err := contextError(ctx); err != nil {
		return false, err
	}
	wire, err := store.encodeRecord(runID, value)
	if err != nil {
		return false, err
	}
	// This final preflight defines the ambiguity boundary. Before it, no
	// filesystem writer has been invoked and cancellation is proven retryable.
	if err = contextError(ctx); err != nil {
		return false, err
	}
	if err = store.writeFile(store.stateName(runID), wire); err != nil {
		return true, ErrUnavailable
	}
	return true, nil
}

func (store *Store) encodeRecord(runID string, value record) ([]byte, error) {
	if err := store.validateRecord(value, runID); err != nil {
		return nil, ErrState
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, ErrState
	}
	mac := hmac.New(sha256.New, store.key[:])
	writeMACPart(mac, []byte(recordDomain))
	writeMACPart(mac, encoded)
	wire, err := json.Marshal(signedRecord{Record: value, MAC: hex.EncodeToString(mac.Sum(nil))})
	if err != nil || len(wire) > maximumRecordSize {
		return nil, ErrState
	}
	return append(wire, '\n'), nil
}

func (store *Store) readRecord(runID string) (record, error) {
	wire, err := store.directory.readFile(store.stateName(runID), maximumRecordSize)
	if err != nil {
		return record{}, err
	}
	return store.decodeRecord(runID, wire)
}

func (store *Store) decodeRecord(runID string, wire []byte) (record, error) {
	if len(wire) == 0 || len(wire) > maximumRecordSize {
		return record{}, ErrVerification
	}
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.DisallowUnknownFields()
	var signed signedRecord
	if err := decoder.Decode(&signed); err != nil {
		return record{}, ErrVerification
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return record{}, ErrVerification
	}
	encoded, err := json.Marshal(signed.Record)
	if err != nil {
		return record{}, ErrVerification
	}
	claimed, err := hex.DecodeString(signed.MAC)
	if err != nil || len(claimed) != sha256.Size {
		return record{}, ErrVerification
	}
	mac := hmac.New(sha256.New, store.key[:])
	writeMACPart(mac, []byte(recordDomain))
	writeMACPart(mac, encoded)
	if !hmac.Equal(claimed, mac.Sum(nil)) || store.validateRecord(signed.Record, runID) != nil {
		return record{}, ErrVerification
	}
	return signed.Record, nil
}

func writeMACPart(target io.Writer, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = target.Write(size[:])
	_, _ = target.Write(value)
}

func (store *Store) validateRecord(value record, runID string) error {
	if value.Version != recordVersion || value.ScopeID != hex.EncodeToString(store.scopeID[:]) ||
		value.AuthorityGeneration != store.authorityGeneration || value.RunID != runID || value.RunGeneration == 0 {
		return ErrVerification
	}
	if err := validateRunID(value.RunID); err != nil {
		return err
	}
	switch value.Phase {
	case PhaseActive, PhaseImporting, PhaseCompleted, PhaseFailed, PhaseCancelled:
		if value.Snapshot != nil {
			return ErrVerification
		}
	case PhaseSuspended:
		if !validSnapshotRecord(value.Snapshot) {
			return ErrVerification
		}
	default:
		return ErrVerification
	}
	return nil
}

func validSnapshotRecord(value *snapshotRecord) bool {
	if value == nil || value.Format != enginev1.SnapshotFormat || value.LastSequence == 0 || value.LastSequence == math.MaxUint64 ||
		value.Lifecycle != int32(enginev1.SnapshotLifecycle_SNAPSHOT_LIFECYCLE_SUSPENDED) {
		return false
	}
	digest, digestErr := hex.DecodeString(value.PayloadSHA)
	authority, authorityErr := hex.DecodeString(value.AuthorityMAC)
	return digestErr == nil && authorityErr == nil && len(digest) == sha256.Size && len(authority) == sha256.Size
}

func (store *Store) matchesSuspended(value record, snapshot *enginev1.SnapshotEnvelope) bool {
	return value.Phase == PhaseSuspended && value.Snapshot != nil &&
		value.ScopeID == hex.EncodeToString(store.scopeID[:]) &&
		value.AuthorityGeneration == snapshot.GetAuthority().GetGeneration() &&
		value.Snapshot.Format == snapshot.GetFormat() &&
		value.Snapshot.LastSequence == snapshot.GetLastSequence() &&
		value.Snapshot.Lifecycle == int32(snapshot.GetLifecycle()) &&
		value.Snapshot.PayloadSHA == hex.EncodeToString(snapshot.GetSha256()) &&
		value.Snapshot.AuthorityMAC == hex.EncodeToString(snapshot.GetAuthority().GetHmacSha256())
}

func validateContextAndRun(ctx context.Context, runID string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	return validateRunID(runID)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return ErrConfiguration
	}
	return context.Cause(ctx)
}

func validateRunID(runID string) error {
	if runID == "" || runID != strings.TrimSpace(runID) || len(runID) > 128 {
		return ErrConfiguration
	}
	for _, character := range runID {
		if unicode.IsControl(character) {
			return ErrConfiguration
		}
	}
	return nil
}

func classifyLockError(err error) error {
	if errors.Is(err, errLockBusy) {
		return ErrBusy
	}
	return ErrUnavailable
}

func allZero(value []byte) bool {
	var combined byte
	for _, item := range value {
		combined |= item
	}
	return combined == 0
}

func (store *Store) markUncertain(runID string) {
	store.mu.Lock()
	if store.uncertain == nil {
		store.uncertain = make(map[string]struct{})
	}
	store.uncertain[runID] = struct{}{}
	store.mu.Unlock()
}

func (store *Store) isUncertain(runID string) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	_, found := store.uncertain[runID]
	return found
}

func (store *Store) beginLease() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closing || store.closed || store.directory == nil {
		return ErrClosed
	}
	store.leases++
	return nil
}

func (store *Store) endLease() error {
	store.mu.Lock()
	if store.leases == 0 {
		store.mu.Unlock()
		return ErrState
	}
	store.leases--
	if store.leases != 0 || !store.closing || store.closed {
		store.mu.Unlock()
		return nil
	}
	store.closed = true
	store.mu.Unlock()
	return store.finishDirectoryClose()
}

func (store *Store) finishDirectoryClose() error {
	err := store.directory.close()
	store.mu.Lock()
	clear(store.key[:])
	if err != nil {
		store.closeErr = ErrUnavailable
	}
	close(store.closeDone)
	result := store.closeErr
	store.mu.Unlock()
	return result
}
