package agent

import (
	"errors"
	"fmt"
	"sync"
)

const (
	defaultRunIdentityEntries uint32 = 65_536
	defaultRunIdentityBytes   uint64 = 16 << 20
	maximumRunIdentityEntries uint32 = 1_048_576
	maximumRunIdentityBytes   uint64 = 256 << 20
	runIdentityEntryBytes     uint64 = 128
)

var (
	// ErrRunIdentityCapacity reports that the exact in-memory identity ledger
	// cannot admit another reservation without exceeding its configured bound.
	ErrRunIdentityCapacity = errors.New("agent run identity capacity is exhausted")
	errRunIdentityState    = errors.New("agent run identity lifecycle is invalid")
)

// RunIdentityLimits bounds the exact process-lifetime identity ledger. The
// ledger never evicts or approximates entries: only an explicit terminal
// retirement capability can remove a tombstone.
type RunIdentityLimits struct {
	entries uint32
	bytes   uint64
}

// NewRunIdentityLimits constructs exact count and charged-byte bounds.
func NewRunIdentityLimits(entries uint32, bytes uint64) (RunIdentityLimits, error) {
	limits := RunIdentityLimits{entries: entries, bytes: bytes}
	if err := limits.Validate(); err != nil {
		return RunIdentityLimits{}, err
	}
	return limits, nil
}

// DefaultRunIdentityLimits returns the daemon-oriented conservative bounds.
func DefaultRunIdentityLimits() RunIdentityLimits {
	limits, _ := NewRunIdentityLimits(defaultRunIdentityEntries, defaultRunIdentityBytes)
	return limits
}

// Entries returns the maximum simultaneously retained identities.
func (limits RunIdentityLimits) Entries() uint32 { return limits.entries }

// Bytes returns the maximum charged identity bytes.
func (limits RunIdentityLimits) Bytes() uint64 { return limits.bytes }

// Validate rejects zero, internally impossible, and excessively large bounds.
func (limits RunIdentityLimits) Validate() error {
	if limits.entries < 1 || limits.entries > maximumRunIdentityEntries {
		return fmt.Errorf("agent run identity entries must be between 1 and %d", maximumRunIdentityEntries)
	}
	if limits.bytes < runIdentityEntryBytes+1 || limits.bytes > maximumRunIdentityBytes {
		return fmt.Errorf("agent run identity bytes must be between %d and %d", runIdentityEntryBytes+1, maximumRunIdentityBytes)
	}
	return nil
}

// RunIdentityStats is an immutable exact ledger observation.
type RunIdentityStats struct {
	limits     RunIdentityLimits
	reserved   uint32
	active     uint32
	tombstones uint32
	bytes      uint64
}

// Limits returns the configured ledger bounds.
func (stats RunIdentityStats) Limits() RunIdentityLimits { return stats.limits }

// Reserved returns the number of uncommitted prepared identities.
func (stats RunIdentityStats) Reserved() uint32 { return stats.reserved }

// Active returns the number of committed nonterminal identities.
func (stats RunIdentityStats) Active() uint32 { return stats.active }

// Tombstones returns the number of terminal identities awaiting retirement.
func (stats RunIdentityStats) Tombstones() uint32 { return stats.tombstones }

// Entries returns the exact number of retained identities.
func (stats RunIdentityStats) Entries() uint32 {
	return stats.reserved + stats.active + stats.tombstones
}

// Bytes returns the exact charged bytes retained by the ledger.
func (stats RunIdentityStats) Bytes() uint64 { return stats.bytes }

// RunIdentityCapacityError reports the exact exhausted dimension without
// exposing any retained run identity.
type RunIdentityCapacityError struct {
	resource string
	limit    uint64
	observed uint64
	stats    RunIdentityStats
}

func (failure *RunIdentityCapacityError) Error() string {
	if failure == nil {
		return ErrRunIdentityCapacity.Error()
	}
	return fmt.Sprintf("%s: %s limit %d, observed %d", ErrRunIdentityCapacity, failure.resource, failure.limit, failure.observed)
}

// Unwrap makes the failure match ErrRunIdentityCapacity.
func (failure *RunIdentityCapacityError) Unwrap() error { return ErrRunIdentityCapacity }

// Resource returns entries or bytes.
func (failure *RunIdentityCapacityError) Resource() string {
	if failure == nil {
		return ""
	}
	return failure.resource
}

// Limit returns the exhausted resource's configured bound.
func (failure *RunIdentityCapacityError) Limit() uint64 {
	if failure == nil {
		return 0
	}
	return failure.limit
}

// Observed returns the exact value the rejected reservation would require.
func (failure *RunIdentityCapacityError) Observed() uint64 {
	if failure == nil {
		return 0
	}
	return failure.observed
}

// Stats returns the exact ledger observation before rejected admission.
func (failure *RunIdentityCapacityError) Stats() RunIdentityStats {
	if failure == nil {
		return RunIdentityStats{}
	}
	return failure.stats
}

type runIdentityState uint8

const (
	runIdentityReserved runIdentityState = iota + 1
	runIdentityActive
	runIdentityTombstone
)

type runIdentityRecord struct {
	token  uint64
	charge uint64
	state  runIdentityState
}

type runIdentityLedger struct {
	limits     RunIdentityLimits
	records    map[string]runIdentityRecord
	nextToken  uint64
	bytes      uint64
	reserved   uint32
	active     uint32
	tombstones uint32
}

func newRunIdentityLedger(limits RunIdentityLimits) *runIdentityLedger {
	return &runIdentityLedger{limits: limits, records: make(map[string]runIdentityRecord)}
}

func (ledger *runIdentityLedger) stats() RunIdentityStats {
	return RunIdentityStats{
		limits: ledger.limits, reserved: ledger.reserved, active: ledger.active,
		tombstones: ledger.tombstones, bytes: ledger.bytes,
	}
}

func (ledger *runIdentityLedger) reserve(runID string, kind preparedExecutionKind) (uint64, error) {
	if _, duplicate := ledger.records[runID]; duplicate {
		return 0, preparedDuplicateError(kind, runID)
	}
	charge := runIdentityEntryBytes + uint64(len(runID))
	stats := ledger.stats()
	if stats.Entries()+1 > ledger.limits.entries {
		return 0, &RunIdentityCapacityError{
			resource: "entries", limit: uint64(ledger.limits.entries),
			observed: uint64(stats.Entries()) + 1, stats: stats,
		}
	}
	if ledger.bytes+charge > ledger.limits.bytes {
		return 0, &RunIdentityCapacityError{
			resource: "bytes", limit: ledger.limits.bytes,
			observed: ledger.bytes + charge, stats: stats,
		}
	}
	if ledger.nextToken == ^uint64(0) {
		return 0, errors.New("agent run identity token space is exhausted")
	}
	ledger.nextToken++
	ledger.records[runID] = runIdentityRecord{token: ledger.nextToken, charge: charge, state: runIdentityReserved}
	ledger.bytes += charge
	ledger.reserved++
	return ledger.nextToken, nil
}

func (ledger *runIdentityLedger) transition(runID string, token uint64, from, to runIdentityState) error {
	record, found := ledger.records[runID]
	if !found || record.token != token || record.state != from {
		return errRunIdentityState
	}
	if to != runIdentityActive && to != runIdentityTombstone {
		return errRunIdentityState
	}
	switch from {
	case runIdentityReserved:
		ledger.reserved--
	case runIdentityActive:
		ledger.active--
	case runIdentityTombstone:
		ledger.tombstones--
	}
	switch to {
	case runIdentityActive:
		ledger.active++
	case runIdentityTombstone:
		ledger.tombstones++
	}
	record.state = to
	ledger.records[runID] = record
	return nil
}

func (ledger *runIdentityLedger) abort(runID string, token uint64) error {
	record, found := ledger.records[runID]
	if !found || record.token != token || record.state != runIdentityReserved {
		return errRunIdentityState
	}
	delete(ledger.records, runID)
	ledger.reserved--
	ledger.bytes -= record.charge
	return nil
}

func (ledger *runIdentityLedger) retire(runID string, token uint64) error {
	record, found := ledger.records[runID]
	if !found || record.token != token || record.state != runIdentityTombstone {
		return errRunIdentityState
	}
	delete(ledger.records, runID)
	ledger.tombstones--
	ledger.bytes -= record.charge
	return nil
}

// RunIdentityRetirement is an opaque, exact-generation capability. Calling
// Retire is appropriate only after an external durable authority has made all
// prior resumable snapshots for the terminal run permanently non-importable.
// An early or stale call fails closed; a successful call is idempotent.
type RunIdentityRetirement struct {
	mu      sync.Mutex
	engine  *Engine
	runID   string
	token   uint64
	retired bool
}

// Retire removes exactly this terminal generation's tombstone.
func (retirement *RunIdentityRetirement) Retire() error {
	if retirement == nil || retirement.engine == nil {
		return errors.New("agent run identity retirement is nil")
	}
	retirement.mu.Lock()
	defer retirement.mu.Unlock()
	if retirement.retired {
		return nil
	}
	if err := retirement.engine.retireRunIdentity(retirement.runID, retirement.token); err != nil {
		return err
	}
	retirement.retired = true
	return nil
}

func (*RunIdentityRetirement) String() string   { return "agent run identity retirement" }
func (*RunIdentityRetirement) GoString() string { return "agent.RunIdentityRetirement{redacted}" }

func (engine *Engine) reserveRunIdentity(runID string, kind preparedExecutionKind) (uint64, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closed {
		return 0, errors.New("agent engine is closed")
	}
	return engine.identities.reserve(runID, kind)
}

func (engine *Engine) abortRunIdentityReservation(runID string, token uint64) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.identities.abort(runID, token)
}

func (engine *Engine) retireRunIdentity(runID string, token uint64) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.identities.retire(runID, token)
}

// RunIdentityStats returns one exact lock-consistent ledger observation.
func (engine *Engine) RunIdentityStats() RunIdentityStats {
	if engine == nil {
		return RunIdentityStats{}
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.identities.stats()
}
