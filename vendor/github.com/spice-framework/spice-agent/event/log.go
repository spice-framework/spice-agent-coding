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
	resource      string
	limit         uint64
	observed      uint64
}

func (failure *ResourceExhaustedError) Error() string {
	return fmt.Sprintf(
		"event delivery exceeded %s limit %d with %d after sequence %d",
		failure.resource, failure.limit, failure.observed, failure.LastDelivered,
	)
}

// Resource identifies the exact safe protocol resource that was exhausted.
func (failure *ResourceExhaustedError) Resource() string {
	if failure == nil {
		return ""
	}
	return failure.resource
}

// Limit returns the exact configured bound that was exceeded.
func (failure *ResourceExhaustedError) Limit() uint64 {
	if failure == nil {
		return 0
	}
	return failure.limit
}

// Observed returns the exact count or byte size refused by the bound.
func (failure *ResourceExhaustedError) Observed() uint64 {
	if failure == nil {
		return 0
	}
	return failure.observed
}

func newResourceExhausted(
	lastDelivered uint64,
	maxEvents int,
	maxBytes int,
	resource string,
	limit uint64,
	observed uint64,
) *ResourceExhaustedError {
	return &ResourceExhaustedError{
		LastDelivered: lastDelivered, MaxEvents: maxEvents, MaxBytes: maxBytes,
		resource: resource, limit: limit, observed: observed,
	}
}

// nonnegativeUint64 converts count and byte values only after rejecting an
// impossible negative internal value. Returning zero fails closed when the
// value is later translated into protocol overload facts.
func nonnegativeUint64(value int) uint64 {
	if value < 0 {
		return 0
	}
	return uint64(value) // #nosec G115 -- negativity is rejected immediately above.
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

// ReplayRequest bounds one atomic retained-history page and optionally asks to
// register a live tail when that page reaches the captured latest sequence.
type ReplayRequest struct {
	AfterSequence uint64
	MaxEvents     int
	MaxBytes      int
	Tail          bool
}

// ReplayPage is one gap-free page captured with its retained bounds. Tail is
// non-nil only when Tailing is true and future appends were registered under
// the same log lock that captured Events and LatestSequence.
type ReplayPage struct {
	EarliestSequence uint64
	LatestSequence   uint64
	PageLastSequence uint64
	Events           []Envelope
	HasMore          bool
	Tailing          bool
	Tail             *Subscription
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
		return newResourceExhausted(
			log.lastSequence, maxEvents, maxBytes, "event_log_bytes",
			nonnegativeUint64(maxBytes), nonnegativeUint64(size),
		)
	}
	for len(log.entries) >= maxEvents || log.bytes > maxBytes-size {
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

// Replay atomically captures retained bounds and one count-and-byte-bounded
// page after a cursor. When Tail is requested on the final page, a live
// subscription is registered before the log lock is released, so an append
// cannot fall between replay and tail delivery.
func (log *Log) Replay(ctx context.Context, request ReplayRequest) (ReplayPage, error) {
	if ctx == nil {
		return ReplayPage{}, errors.New("event replay context must not be nil")
	}
	if log == nil {
		return ReplayPage{}, errors.New("event log is nil")
	}
	if err := ctx.Err(); err != nil {
		return ReplayPage{}, err
	}
	if request.MaxEvents < 1 || request.MaxEvents > log.limits.SubscriberMaxEvents {
		return ReplayPage{}, fmt.Errorf("event replay max events must be between 1 and %d", log.limits.SubscriberMaxEvents)
	}
	if request.MaxBytes < 1 || request.MaxBytes > log.limits.SubscriberMaxBytes {
		return ReplayPage{}, fmt.Errorf("event replay max bytes must be between 1 and %d", log.limits.SubscriberMaxBytes)
	}

	log.mu.Lock()
	defer log.mu.Unlock()
	entries, page, err := log.replayPageLocked(request)
	if err != nil {
		return ReplayPage{}, err
	}
	page.Events = make([]Envelope, len(entries))
	for index, entry := range entries {
		page.Events[index] = entry.envelope
	}
	if request.Tail && !page.HasMore && !log.closed {
		page.Tail = log.registerSubscriptionLocked(ctx, page.PageLastSequence, nil)
		page.Tailing = true
	}
	return page, nil
}

func (log *Log) replayPageLocked(request ReplayRequest) ([]logEntry, ReplayPage, error) {
	earliest, latest := log.boundsLocked()
	page := ReplayPage{
		EarliestSequence: earliest,
		LatestSequence:   latest,
		PageLastSequence: request.AfterSequence,
	}
	if replayCursorOutside(request.AfterSequence, earliest, latest) {
		recovery := latest
		if request.AfterSequence < earliest {
			recovery = earliest - 1
		}
		return nil, ReplayPage{}, &OutOfRangeError{
			RequestedAfter: request.AfterSequence,
			Earliest:       earliest, Latest: latest, RecoveryAfter: recovery,
		}
	}
	entries := make([]logEntry, 0, min(request.MaxEvents, len(log.entries)))
	bytes := 0
	for _, entry := range log.entries {
		if entry.envelope.Sequence() <= request.AfterSequence {
			continue
		}
		if len(entries) == request.MaxEvents {
			break
		}
		if entry.bytes > request.MaxBytes || bytes > request.MaxBytes-entry.bytes {
			if len(entries) == 0 {
				log.stats.SubscriptionExhaustions++
				return nil, ReplayPage{}, newResourceExhausted(
					request.AfterSequence, request.MaxEvents, request.MaxBytes,
					"event_replay_bytes",
					nonnegativeUint64(request.MaxBytes), nonnegativeUint64(entry.bytes),
				)
			}
			break
		}
		entries = append(entries, entry)
		bytes += entry.bytes
		page.PageLastSequence = entry.envelope.Sequence()
	}
	if page.PageLastSequence < latest {
		if len(entries) == 0 {
			return nil, ReplayPage{}, errors.New("event replay made no progress without an exhausted bound")
		}
		page.HasMore = true
	}
	return entries, page, nil
}

func replayCursorOutside(after, earliest, latest uint64) bool {
	if earliest == latest+1 { // Empty initial [1,0] or imported tail [N+1,N].
		return after != latest
	}
	return after > latest || after < earliest-1
}

func (log *Log) registerSubscriptionLocked(ctx context.Context, after uint64, replay []logEntry) *Subscription {
	log.nextSubID++
	subscription := newSubscription(ctx, after, log.limits, replay)
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
	return subscription
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
	replay, page, err := log.replayPageLocked(ReplayRequest{
		AfterSequence: afterSequence,
		MaxEvents:     log.limits.SubscriberMaxEvents,
		MaxBytes:      log.limits.SubscriberMaxBytes,
		Tail:          true,
	})
	if err != nil {
		return nil, err
	}
	if page.HasMore {
		log.stats.SubscriptionExhaustions++
		exhausted, exhaustedErr := log.replayExhaustionLocked(page.PageLastSequence, replay)
		if exhaustedErr != nil {
			return nil, exhaustedErr
		}
		return nil, exhausted
	}
	return log.registerSubscriptionLocked(ctx, afterSequence, replay), nil
}

func (log *Log) replayExhaustionLocked(
	lastDelivered uint64,
	replay []logEntry,
) (*ResourceExhaustedError, error) {
	if len(replay) >= log.limits.SubscriberMaxEvents {
		return newResourceExhausted(
			lastDelivered, log.limits.SubscriberMaxEvents, log.limits.SubscriberMaxBytes,
			"event_replay_events", nonnegativeUint64(log.limits.SubscriberMaxEvents),
			nonnegativeUint64(len(replay))+1,
		), nil
	}
	var queuedBytes uint64
	for _, entry := range replay {
		queuedBytes += nonnegativeUint64(entry.bytes)
	}
	for _, entry := range log.entries {
		if entry.envelope.Sequence() > lastDelivered {
			return newResourceExhausted(
				lastDelivered, log.limits.SubscriberMaxEvents, log.limits.SubscriberMaxBytes,
				"event_replay_bytes", nonnegativeUint64(log.limits.SubscriberMaxBytes),
				queuedBytes+nonnegativeUint64(entry.bytes),
			), nil
		}
	}
	return nil, errors.New("event replay exhausted without a retained recovery entry")
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

func newSubscription(ctx context.Context, after uint64, limits LogLimits, replay []logEntry) *Subscription {
	subscription := &Subscription{
		ctx: ctx, queue: append([]logEntry(nil), replay...), events: make(chan Envelope), done: make(chan struct{}),
		maxEvents: limits.SubscriberMaxEvents, maxBytes: limits.SubscriberMaxBytes,
		lastDelivered: after, abort: make(chan struct{}),
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
	if len(subscription.queue) >= subscription.maxEvents {
		subscription.terminateLocked(newResourceExhausted(
			subscription.lastDelivered, subscription.maxEvents, subscription.maxBytes,
			"event_subscription_events", nonnegativeUint64(subscription.maxEvents),
			nonnegativeUint64(len(subscription.queue))+1,
		))
		subscription.cond.Broadcast()
		return true
	}
	if entry.bytes > subscription.maxBytes || subscription.queuedBytes > subscription.maxBytes-entry.bytes {
		subscription.terminateLocked(newResourceExhausted(
			subscription.lastDelivered, subscription.maxEvents, subscription.maxBytes,
			"event_subscription_bytes", nonnegativeUint64(subscription.maxBytes),
			nonnegativeUint64(subscription.queuedBytes)+nonnegativeUint64(entry.bytes),
		))
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
