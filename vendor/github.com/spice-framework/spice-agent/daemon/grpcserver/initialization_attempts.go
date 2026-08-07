package grpcserver

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"

	enginev1 "github.com/spice-framework/spice-agent/engine/v1"
	"google.golang.org/protobuf/proto"
)

const maximumInitializationWaitersPerAttempt = 64

var (
	errInitializationAttemptConflict    = errors.New("initialization attempt identity conflicts with another request")
	errInitializationAttemptCapacity    = errors.New("initialization attempt reservation capacity is exhausted")
	errInitializationAttemptUnavailable = errors.New("initialization attempt is unavailable")
)

// initializationFingerprint is the deterministic semantic request identity.
// Transport authentication is gRPC metadata and is therefore deliberately
// absent; the caller-generated attempt identity is cleared before encoding.
type initializationFingerprint [sha256.Size]byte

type initializationAttemptRecord struct {
	fingerprint initializationFingerprint
	done        chan struct{}
	response    *enginev1.InitializeResponse
	waiters     int
	terminal    bool
}

type initializationAttemptLease struct {
	registry *negotiatedSessionRegistry
	id       string
	record   *initializationAttemptRecord
	finished bool
}

type initializationAttemptReservation struct {
	lease    *initializationAttemptLease
	response *enginev1.InitializeResponse
	wait     *initializationAttemptRecord
}

func fingerprintInitializeRequest(request *enginev1.InitializeRequest) (initializationFingerprint, error) {
	if request == nil {
		return initializationFingerprint{}, errNegotiatedSessionInvalid
	}
	semantic := proto.CloneOf(request)
	semantic.InitializationAttemptId = nil
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(semantic)
	if err != nil {
		return initializationFingerprint{}, err
	}
	return sha256.Sum256(encoded), nil
}

// reserveInitializationAttempt returns either an owning lease or the exact
// response committed by an earlier byte-identical request. Concurrent exact
// duplicates wait on one bounded, context-aware record; they never enter the
// SessionStore allocation/CAS path independently.
func (registry *negotiatedSessionRegistry) reserveInitializationAttempt(
	ctx context.Context,
	attemptID []byte,
	fingerprint initializationFingerprint,
) (*initializationAttemptLease, *enginev1.InitializeResponse, error) {
	if registry == nil {
		return nil, nil, errNegotiatedSessionClosed
	}
	if ctx == nil || enginev1.ValidateInitializationAttemptID(attemptID) != nil {
		return nil, nil, errNegotiatedSessionInvalid
	}
	if err := context.Cause(ctx); err != nil {
		return nil, nil, err
	}
	id := string(slices.Clone(attemptID))
	for {
		if err := context.Cause(ctx); err != nil {
			return nil, nil, err
		}
		reservation, err := registry.inspectInitializationAttempt(id, fingerprint)
		if err != nil {
			return nil, nil, err
		}
		if reservation.lease != nil || reservation.response != nil {
			return reservation.lease, reservation.response, nil
		}
		if reservation.wait == nil {
			continue
		}
		response, err := registry.waitForInitializationAttempt(ctx, reservation.wait)
		if response != nil || err != nil {
			return nil, response, err
		}
		// The owner aborted before mutation; race to become the next owner.
	}
}

func (registry *negotiatedSessionRegistry) inspectInitializationAttempt(
	id string,
	fingerprint initializationFingerprint,
) (initializationAttemptReservation, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closedLocked() {
		return initializationAttemptReservation{}, errNegotiatedSessionClosed
	}
	record, exists := registry.attempts[id]
	if !exists {
		return registry.createInitializationAttemptLocked(id, fingerprint)
	}
	if record.fingerprint != fingerprint {
		return initializationAttemptReservation{}, errInitializationAttemptConflict
	}
	if record.terminal {
		return initializationAttemptReservation{response: proto.CloneOf(record.response)}, nil
	}
	if record.waiters >= maximumInitializationWaitersPerAttempt {
		return initializationAttemptReservation{}, &negotiatedCapacityError{
			target:   errNegotiatedSessionWaiterLimit,
			resource: "initialization attempt waiters",
			limit:    maximumInitializationWaitersPerAttempt,
		}
	}
	record.waiters++
	return initializationAttemptReservation{wait: record}, nil
}

func (registry *negotiatedSessionRegistry) createInitializationAttemptLocked(
	id string,
	fingerprint initializationFingerprint,
) (initializationAttemptReservation, error) {
	if registry.pendingAttempts >= registry.maximum {
		return initializationAttemptReservation{}, &negotiatedCapacityError{
			target:   errInitializationAttemptCapacity,
			resource: "initialization attempt reservations",
			limit:    uint64(registry.maximum), // #nosec G115 -- construction bounds maximum to 1..4096.
		}
	}
	record := &initializationAttemptRecord{
		fingerprint: fingerprint,
		done:        make(chan struct{}),
	}
	registry.attempts[id] = record
	registry.pendingAttempts++
	return initializationAttemptReservation{
		lease: &initializationAttemptLease{registry: registry, id: id, record: record},
	}, nil
}

func (registry *negotiatedSessionRegistry) waitForInitializationAttempt(
	ctx context.Context,
	record *initializationAttemptRecord,
) (*enginev1.InitializeResponse, error) {
	waitErr := registry.waitForInitializationAttemptSignal(ctx, record.done)
	registry.mu.Lock()
	if record.waiters > 0 {
		record.waiters--
	}
	committed := record.terminal && record.response != nil
	response := proto.CloneOf(record.response)
	registry.mu.Unlock()
	// A committed response wins a cancellation race: returning it removes
	// uncertainty while preserving the caller's already-completed outcome.
	if committed {
		return response, nil
	}
	return nil, waitErr
}

func (registry *negotiatedSessionRegistry) waitForInitializationAttemptSignal(
	ctx context.Context,
	done <-chan struct{},
) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-registry.root.Done():
		return errNegotiatedSessionClosed
	case <-registry.closedCh:
		return errNegotiatedSessionClosed
	}
}

// commit retains a fresh creation response and the latest reconnect response
// for each active negotiated session. Pending records are never evicted. The
// map is therefore bounded by two committed records per session plus at most
// one pending record per configured session.
func (lease *initializationAttemptLease) commit(
	response *enginev1.InitializeResponse,
	reconnect bool,
) error {
	if lease == nil || lease.registry == nil || lease.record == nil || lease.finished || response == nil {
		return errInitializationAttemptUnavailable
	}
	validated := proto.CloneOf(response)
	if enginev1.ValidateInitializeResponse(validated) != nil ||
		string(validated.GetInitializationAttemptId()) != lease.id {
		return errNegotiatedSessionInvalid
	}
	registry := lease.registry
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return lease.commitLocked(validated, reconnect)
}

func (lease *initializationAttemptLease) commitLocked(
	validated *enginev1.InitializeResponse,
	reconnect bool,
) error {
	registry := lease.registry
	if registry.closedLocked() || registry.attempts[lease.id] != lease.record || lease.record.terminal {
		return errInitializationAttemptUnavailable
	}
	if validated == nil || string(validated.GetInitializationAttemptId()) != lease.id {
		return errNegotiatedSessionInvalid
	}
	entry, exists := registry.entries[validated.GetClientId()]
	if !exists || entry.session.Epoch() != validated.GetOwnershipEpoch() {
		return errInitializationAttemptUnavailable
	}
	if reconnect {
		if previous := entry.reconnectAttempt; previous != "" && previous != lease.id &&
			previous != entry.creationAttempt {
			registry.removeCommittedAttemptLocked(previous)
		}
		entry.reconnectAttempt = lease.id
	} else {
		if entry.creationAttempt != "" && entry.creationAttempt != lease.id {
			return errInitializationAttemptConflict
		}
		entry.creationAttempt = lease.id
	}
	registry.entries[validated.GetClientId()] = entry
	lease.record.response = validated
	lease.record.terminal = true
	if registry.pendingAttempts > 0 {
		registry.pendingAttempts--
	}
	close(lease.record.done)
	lease.finished = true
	return nil
}

// abort releases a reservation only while no session mutation was committed.
// Waiters wake and may elect a new owner for the same exact request.
func (lease *initializationAttemptLease) abort() {
	if lease == nil || lease.registry == nil || lease.record == nil || lease.finished {
		return
	}
	registry := lease.registry
	registry.mu.Lock()
	if registry.attempts[lease.id] == lease.record && !lease.record.terminal {
		delete(registry.attempts, lease.id)
		lease.record.terminal = true
		if registry.pendingAttempts > 0 {
			registry.pendingAttempts--
		}
		close(lease.record.done)
	}
	lease.finished = true
	registry.mu.Unlock()
}

func (registry *negotiatedSessionRegistry) removeCommittedAttemptLocked(id string) {
	record := registry.attempts[id]
	if record != nil && record.terminal && record.response != nil {
		delete(registry.attempts, id)
	}
}

func (registry *negotiatedSessionRegistry) abortAttemptsLocked() {
	for id, record := range registry.attempts {
		if !record.terminal {
			record.terminal = true
			close(record.done)
		}
		delete(registry.attempts, id)
	}
	registry.pendingAttempts = 0
}
