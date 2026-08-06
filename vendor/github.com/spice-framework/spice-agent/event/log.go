package event

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
)

// LogLimits bound authoritative history and every independent subscriber.
type LogLimits struct {
	MaxEvents             int
	MaxBytes              int
	TerminalReserveEvents int
	TerminalReserveBytes  int
	SubscriberMaxEvents   int
	SubscriberMaxBytes    int
}

// DefaultLogLimits are conservative in-process architecture-proof bounds.
func DefaultLogLimits() LogLimits {
	return LogLimits{
		MaxEvents:             8192,
		MaxBytes:              32 << 20,
		TerminalReserveEvents: 4,
		TerminalReserveBytes:  16 << 10,
		SubscriberMaxEvents:   1024,
		SubscriberMaxBytes:    4 << 20,
	}
}

// OutOfRangeError reports a replay cursor outside retained history.
type OutOfRangeError struct {
	RequestedAfter uint64
	Earliest       uint64
	Latest         uint64
	RecoveryAfter  uint64
}

func (failure *OutOfRangeError) Error() string {
	return fmt.Sprintf("event replay after %d is outside retained range [%d,%d]; recover after %d", failure.RequestedAfter, failure.Earliest, failure.Latest, failure.RecoveryAfter)
}

// ResourceExhaustedError reports a subscriber or authoritative bound.
type ResourceExhaustedError struct {
	LastDelivered uint64
	MaxEvents     int
	MaxBytes      int
}

func (failure *ResourceExhaustedError) Error() string {
	return fmt.Sprintf("event delivery exceeded %d events or %d bytes after sequence %d", failure.MaxEvents, failure.MaxBytes, failure.LastDelivered)
}

type logEntry struct {
	envelope Envelope
	bytes    int
}

// LogStats is an immutable bounded-replay health snapshot.
type LogStats struct {
	RetainedEvents            uint64
	RetainedBytes             uint64
	EvictedEvents             uint64
	EvictedBytes              uint64
	AuthoritativeExhaustions  uint64
	SubscriptionExhaustions   uint64
	SlowSubscriberDisconnects uint64
}

// Log is a count-and-byte-bounded authoritative per-run replay log.
type Log struct {
	mu           sync.Mutex
	runID        string
	limits       LogLimits
	entries      []logEntry
	bytes        int
	lastSequence uint64
	closed       bool
	nextSubID    uint64
	subscribers  map[uint64]*Subscription
	stats        LogStats
}

// NewLog validates limits without starting background work.
func NewLog(runID string, limits LogLimits) (*Log, error) {
	return NewLogAfter(runID, 0, limits)
}

// NewLogAfter constructs an empty tail log continuing after a persisted cursor.
func NewLogAfter(runID string, lastSequence uint64, limits LogLimits) (*Log, error) {
	if runID == "" || runID != strings.TrimSpace(runID) {
		return nil, errors.New("event log requires a run ID without surrounding whitespace")
	}
	if limits.MaxEvents < 2 || limits.MaxBytes < 1024 ||
		limits.TerminalReserveEvents < 1 || limits.TerminalReserveEvents >= limits.MaxEvents ||
		limits.TerminalReserveBytes < 1 || limits.TerminalReserveBytes >= limits.MaxBytes ||
		limits.SubscriberMaxEvents < 1 || limits.SubscriberMaxBytes < 1 {
		return nil, errors.New("event log limits are invalid")
	}
	if lastSequence == math.MaxUint64 {
		return nil, errors.New("event log cursor has no representable next sequence")
	}
	return &Log{runID: runID, limits: limits, lastSequence: lastSequence, subscribers: make(map[uint64]*Subscription)}, nil
}

// Append persists one next-sequence event and then offers it to live subscribers.
func (log *Log) Append(envelope Envelope) error {
	if log == nil {
		return errors.New("event log is nil")
	}
	size := envelope.EncodedSize()
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.closed {
		return errors.New("event log is closed")
	}
	if envelope.RunID() != log.runID {
		return fmt.Errorf("event run %q does not match log run %q", envelope.RunID(), log.runID)
	}
	if log.lastSequence == math.MaxUint64 || envelope.Sequence() != log.lastSequence+1 {
		return fmt.Errorf("event sequence %d does not follow %d", envelope.Sequence(), log.lastSequence)
	}
	maxEvents, maxBytes := log.limits.MaxEvents, log.limits.MaxBytes
	if !envelope.Terminal() {
		maxEvents -= log.limits.TerminalReserveEvents
		maxBytes -= log.limits.TerminalReserveBytes
	}
	if size > maxBytes {
		log.stats.AuthoritativeExhaustions++
		return &ResourceExhaustedError{LastDelivered: log.lastSequence, MaxEvents: maxEvents, MaxBytes: maxBytes}
	}
	for len(log.entries) >= maxEvents || log.bytes+size > maxBytes {
		log.stats.EvictedEvents++
		log.stats.EvictedBytes += uint64(log.entries[0].bytes) // #nosec G115 -- entry size is positive and bounded by MaxBytes.
		log.bytes -= log.entries[0].bytes
		log.entries = log.entries[1:]
	}
	entry := logEntry{envelope: envelope, bytes: size}
	log.entries = append(log.entries, entry)
	log.bytes += size
	log.lastSequence = envelope.Sequence()
	for _, subscriber := range log.subscribers {
		if subscriber.offer(entry) {
			log.stats.SlowSubscriberDisconnects++
		}
	}
	return nil
}

// Stats returns a race-safe immutable replay health snapshot.
func (log *Log) Stats() LogStats {
	if log == nil {
		return LogStats{}
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	result := log.stats
	result.RetainedEvents = uint64(len(log.entries)) // #nosec G115 -- slice length is non-negative and representable by uint64.
	result.RetainedBytes = uint64(log.bytes)         // #nosec G115 -- retained bytes are non-negative and bounded by MaxBytes.
	return result
}

// Bounds returns the retained earliest and latest sequence. An empty log is [1,0].
func (log *Log) Bounds() (uint64, uint64) {
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.boundsLocked()
}

func (log *Log) boundsLocked() (uint64, uint64) {
	if len(log.entries) == 0 {
		if log.lastSequence == math.MaxUint64 {
			return math.MaxUint64, math.MaxUint64
		}
		return log.lastSequence + 1, log.lastSequence
	}
	return log.entries[0].envelope.Sequence(), log.lastSequence
}

// Subscribe atomically replays entries after a cursor and tails future appends.
func (log *Log) Subscribe(ctx context.Context, afterSequence uint64) (*Subscription, error) {
	if ctx == nil {
		return nil, errors.New("event subscription context must not be nil")
	}
	if log == nil {
		return nil, errors.New("event log is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	earliest, latest := log.boundsLocked()
	if afterSequence > latest || (latest != 0 && earliest > 1 && afterSequence < earliest-1) {
		recovery := latest
		if afterSequence <= latest {
			recovery = earliest - 1
		}
		return nil, &OutOfRangeError{RequestedAfter: afterSequence, Earliest: earliest, Latest: latest, RecoveryAfter: recovery}
	}
	replay := make([]logEntry, 0)
	replayBytes := 0
	for _, entry := range log.entries {
		if entry.envelope.Sequence() <= afterSequence {
			continue
		}
		replay = append(replay, entry)
		replayBytes += entry.bytes
	}
	if len(replay) > log.limits.SubscriberMaxEvents || replayBytes > log.limits.SubscriberMaxBytes {
		log.stats.SubscriptionExhaustions++
		return nil, &ResourceExhaustedError{LastDelivered: afterSequence, MaxEvents: log.limits.SubscriberMaxEvents, MaxBytes: log.limits.SubscriberMaxBytes}
	}
	log.nextSubID++
	subscription := newSubscription(ctx, afterSequence, log.limits, replay, nil)
	id := log.nextSubID
	subscription.onDone = func() {
		log.mu.Lock()
		delete(log.subscribers, id)
		log.mu.Unlock()
	}
	if log.closed {
		subscription.finish(nil)
	} else {
		log.subscribers[id] = subscription
	}
	go subscription.deliver()
	return subscription, nil
}

// Close completes live subscribers after their queued tail is delivered.
func (log *Log) Close() {
	if log == nil {
		return
	}
	log.mu.Lock()
	if log.closed {
		log.mu.Unlock()
		return
	}
	log.closed = true
	for _, subscriber := range log.subscribers {
		subscriber.finish(nil)
	}
	log.mu.Unlock()
}

// Subscription is one independent bounded replay/tail cursor.
type Subscription struct {
	ctx           context.Context //nolint:containedctx // subscription owns cancellation for its delivery lifetime.
	mu            sync.Mutex
	cond          *sync.Cond
	queue         []logEntry
	queuedBytes   int
	maxEvents     int
	maxBytes      int
	events        chan Envelope
	done          chan struct{}
	terminal      bool
	err           error
	lastDelivered uint64
	onDone        func()
	abort         chan struct{}
	abortOnce     sync.Once
	stopContext   func() bool
}

func newSubscription(ctx context.Context, after uint64, limits LogLimits, replay []logEntry, onDone func()) *Subscription {
	subscription := &Subscription{
		ctx: ctx, queue: append([]logEntry(nil), replay...), events: make(chan Envelope), done: make(chan struct{}),
		maxEvents: limits.SubscriberMaxEvents, maxBytes: limits.SubscriberMaxBytes,
		lastDelivered: after, onDone: onDone, abort: make(chan struct{}),
	}
	for _, entry := range replay {
		subscription.queuedBytes += entry.bytes
	}
	subscription.cond = sync.NewCond(&subscription.mu)
	subscription.stopContext = context.AfterFunc(ctx, func() { subscription.terminate(ctx.Err()) })
	return subscription
}

// Events returns this subscription's ordered stream.
func (subscription *Subscription) Events() <-chan Envelope { return subscription.events }

// Wait reports clean log completion, context cancellation, or typed exhaustion.
func (subscription *Subscription) Wait(ctx context.Context) error {
	if ctx == nil {
		return errors.New("subscription wait context must not be nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-subscription.done:
		subscription.mu.Lock()
		defer subscription.mu.Unlock()
		return subscription.err
	}
}

// LastDelivered returns the last event accepted by the consumer.
func (subscription *Subscription) LastDelivered() uint64 {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	return subscription.lastDelivered
}

func (subscription *Subscription) offer(entry logEntry) bool {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	if subscription.terminal {
		return false
	}
	if len(subscription.queue) >= subscription.maxEvents || subscription.queuedBytes+entry.bytes > subscription.maxBytes {
		subscription.terminateLocked(&ResourceExhaustedError{LastDelivered: subscription.lastDelivered, MaxEvents: subscription.maxEvents, MaxBytes: subscription.maxBytes})
		subscription.cond.Broadcast()
		return true
	}
	subscription.queue = append(subscription.queue, entry)
	subscription.queuedBytes += entry.bytes
	subscription.cond.Signal()
	return false
}

func (subscription *Subscription) finish(err error) {
	subscription.mu.Lock()
	if !subscription.terminal {
		subscription.terminal = true
		subscription.err = err
		subscription.cond.Broadcast()
	}
	subscription.mu.Unlock()
}

func (subscription *Subscription) terminate(err error) {
	subscription.mu.Lock()
	subscription.terminateLocked(err)
	subscription.mu.Unlock()
}

func (subscription *Subscription) terminateLocked(err error) {
	if subscription.terminal {
		return
	}
	subscription.terminal = true
	subscription.err = err
	subscription.queue = nil
	subscription.queuedBytes = 0
	subscription.abortOnce.Do(func() { close(subscription.abort) })
	subscription.cond.Broadcast()
}

func (subscription *Subscription) deliver() {
	subscription.deliverWithAfterSend(nil)
}

func (subscription *Subscription) deliverWithAfterSend(afterSend func()) {
	defer close(subscription.events)
	defer close(subscription.done)
	defer func() {
		if subscription.stopContext != nil {
			subscription.stopContext()
		}
		if subscription.onDone != nil {
			subscription.onDone()
		}
	}()
	for {
		subscription.mu.Lock()
		for len(subscription.queue) == 0 && !subscription.terminal {
			subscription.cond.Wait()
		}
		if len(subscription.queue) == 0 && subscription.terminal {
			subscription.mu.Unlock()
			return
		}
		entry := subscription.queue[0]
		subscription.mu.Unlock()
		select {
		case <-subscription.abort:
			return
		case <-subscription.ctx.Done():
			subscription.terminate(subscription.ctx.Err())
			return
		case subscription.events <- entry.envelope:
		}
		if afterSend != nil {
			afterSend()
		}
		subscription.mu.Lock()
		// Cancellation may terminate the subscription and clear its queue after
		// the send commits but before this goroutine reacquires the lock.
		if len(subscription.queue) != 0 &&
			subscription.queue[0].envelope.Sequence() == entry.envelope.Sequence() {
			subscription.queue = subscription.queue[1:]
			subscription.queuedBytes -= entry.bytes
		}
		subscription.lastDelivered = entry.envelope.Sequence()
		if exhausted, found := errors.AsType[*ResourceExhaustedError](subscription.err); found {
			exhausted.LastDelivered = subscription.lastDelivered
		}
		subscription.mu.Unlock()
	}
}
