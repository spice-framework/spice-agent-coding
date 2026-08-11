// Package cache provides typed cache contracts and a bounded in-memory
// implementation for generated Spice cache decorators.
package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Definition identifies one compiler-owned cache and its module.
type Definition struct {
	ID     string
	Module string
}

// Store is the typed cache dependency used by generated decorators.
type Store[K comparable, V any] interface {
	Get(context.Context, K) (V, bool, error)
	Put(context.Context, K, V, time.Duration) error
	Delete(context.Context, K) error
}

// Operation identifies one observed cache action.
type Operation string

const (
	// OperationGet identifies a lookup.
	OperationGet Operation = "get"
	// OperationPut identifies an insertion or replacement.
	OperationPut Operation = "put"
	// OperationDelete identifies explicit invalidation.
	OperationDelete Operation = "delete"
	// OperationPurge identifies explicit expired-entry removal.
	OperationPurge Operation = "purge"
)

// Observation contains bounded cache metadata. Keys and values are
// intentionally excluded.
type Observation struct {
	Definition Definition
	Operation  Operation
	Duration   time.Duration
	Hit        bool
	Evicted    int
	Removed    int
	Size       int
}

// Observer receives completed cache operations on the caller's goroutine.
type Observer func(context.Context, Observation)

// Snapshot is a concurrency-safe aggregate view.
type Snapshot struct {
	Size      int
	Hits      uint64
	Misses    uint64
	Puts      uint64
	Deletes   uint64
	Evictions uint64
	Expired   uint64
}

type entry[K comparable, V any] struct {
	key       K
	value     V
	expiresAt time.Time
	previous  *entry[K, V]
	next      *entry[K, V]
}

// Memory is a fixed-capacity least-recently-used cache. It has no background
// goroutine; expiration occurs on Get or PurgeExpired.
type Memory[K comparable, V any] struct {
	mu         sync.Mutex
	definition Definition
	capacity   int
	clock      func() time.Time
	observers  []Observer
	items      map[K]*entry[K, V]
	head       *entry[K, V]
	tail       *entry[K, V]
	stats      Snapshot
}

// NewMemory constructs an empty cache. A nil clock selects time.Now.
func NewMemory[K comparable, V any](
	definition Definition,
	capacity int,
	clock func() time.Time,
	observers ...Observer,
) (*Memory[K, V], error) {
	if definition.ID == "" {
		return nil, errors.New("construct cache: cache ID is required")
	}
	if definition.Module == "" {
		return nil, fmt.Errorf("construct cache %q: module is required", definition.ID)
	}
	if capacity < 1 {
		return nil, fmt.Errorf("construct cache %q: capacity must be positive", definition.ID)
	}
	for index, observer := range observers {
		if observer == nil {
			return nil, fmt.Errorf("construct cache %q: observer %d is nil", definition.ID, index)
		}
	}
	if clock == nil {
		clock = time.Now
	}
	return &Memory[K, V]{
		definition: definition,
		capacity:   capacity,
		clock:      clock,
		observers:  append([]Observer(nil), observers...),
		items:      make(map[K]*entry[K, V], capacity),
	}, nil
}

// Get returns and touches an unexpired entry.
func (memory *Memory[K, V]) Get(ctx context.Context, key K) (value V, found bool, err error) {
	if ctx == nil {
		return value, false, errors.New("get cache entry: context is nil")
	}
	if memory == nil {
		return value, false, errors.New("get cache entry: cache is nil")
	}
	if cause := context.Cause(ctx); cause != nil {
		return value, false, fmt.Errorf("get cache entry: %w", cause)
	}
	started := time.Now()
	now := memory.clock()
	removed := 0
	memory.mu.Lock()
	item := memory.items[key]
	switch {
	case item == nil:
		memory.stats.Misses++
	case expired(item, now):
		memory.remove(item)
		memory.stats.Misses++
		memory.stats.Expired++
		removed = 1
	default:
		memory.moveToFront(item)
		memory.stats.Hits++
		value, found = item.value, true
	}
	size := len(memory.items)
	memory.mu.Unlock()
	memory.observe(ctx, Observation{
		Definition: memory.definition,
		Operation:  OperationGet,
		Duration:   time.Since(started),
		Hit:        found,
		Removed:    removed,
		Size:       size,
	})
	return value, found, nil
}

// Put inserts or replaces one value. A zero TTL means no expiration.
func (memory *Memory[K, V]) Put(ctx context.Context, key K, value V, ttl time.Duration) error {
	if ctx == nil {
		return errors.New("put cache entry: context is nil")
	}
	if memory == nil {
		return errors.New("put cache entry: cache is nil")
	}
	if ttl < 0 {
		return errors.New("put cache entry: TTL must not be negative")
	}
	if cause := context.Cause(ctx); cause != nil {
		return fmt.Errorf("put cache entry: %w", cause)
	}
	started := time.Now()
	now := memory.clock()
	expiresAt := time.Time{}
	if ttl > 0 {
		expiresAt = now.Add(ttl)
	}
	evicted := 0
	memory.mu.Lock()
	if item := memory.items[key]; item != nil {
		item.value = value
		item.expiresAt = expiresAt
		memory.moveToFront(item)
	} else {
		item = &entry[K, V]{key: key, value: value, expiresAt: expiresAt}
		memory.items[key] = item
		memory.insertFront(item)
		if len(memory.items) > memory.capacity {
			memory.remove(memory.tail)
			memory.stats.Evictions++
			evicted = 1
		}
	}
	memory.stats.Puts++
	size := len(memory.items)
	memory.mu.Unlock()
	memory.observe(ctx, Observation{
		Definition: memory.definition,
		Operation:  OperationPut,
		Duration:   time.Since(started),
		Evicted:    evicted,
		Size:       size,
	})
	return nil
}

// Delete invalidates a key. Deleting a missing key succeeds.
func (memory *Memory[K, V]) Delete(ctx context.Context, key K) error {
	if ctx == nil {
		return errors.New("delete cache entry: context is nil")
	}
	if memory == nil {
		return errors.New("delete cache entry: cache is nil")
	}
	if cause := context.Cause(ctx); cause != nil {
		return fmt.Errorf("delete cache entry: %w", cause)
	}
	started := time.Now()
	removed := 0
	memory.mu.Lock()
	if item := memory.items[key]; item != nil {
		memory.remove(item)
		memory.stats.Deletes++
		removed = 1
	}
	size := len(memory.items)
	memory.mu.Unlock()
	memory.observe(ctx, Observation{
		Definition: memory.definition,
		Operation:  OperationDelete,
		Duration:   time.Since(started),
		Removed:    removed,
		Size:       size,
	})
	return nil
}

// PurgeExpired removes all entries expired at the caller-controlled clock.
func (memory *Memory[K, V]) PurgeExpired(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, errors.New("purge cache: context is nil")
	}
	if memory == nil {
		return 0, errors.New("purge cache: cache is nil")
	}
	if cause := context.Cause(ctx); cause != nil {
		return 0, fmt.Errorf("purge cache: %w", cause)
	}
	started := time.Now()
	now := memory.clock()
	removed := 0
	memory.mu.Lock()
	for _, item := range memory.items {
		if expired(item, now) {
			memory.remove(item)
			removed++
		}
	}
	memory.stats.Expired += uint64(removed)
	size := len(memory.items)
	memory.mu.Unlock()
	memory.observe(ctx, Observation{
		Definition: memory.definition,
		Operation:  OperationPurge,
		Duration:   time.Since(started),
		Removed:    removed,
		Size:       size,
	})
	return removed, nil
}

// Snapshot returns aggregate statistics without exposing keys or values.
func (memory *Memory[K, V]) Snapshot() Snapshot {
	if memory == nil {
		return Snapshot{}
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	snapshot := memory.stats
	snapshot.Size = len(memory.items)
	return snapshot
}

func expired[K comparable, V any](item *entry[K, V], now time.Time) bool {
	return !item.expiresAt.IsZero() && !item.expiresAt.After(now)
}

func (memory *Memory[K, V]) insertFront(item *entry[K, V]) {
	item.previous = nil
	item.next = memory.head
	if memory.head != nil {
		memory.head.previous = item
	} else {
		memory.tail = item
	}
	memory.head = item
}

func (memory *Memory[K, V]) moveToFront(item *entry[K, V]) {
	if item == memory.head {
		return
	}
	memory.unlink(item)
	memory.insertFront(item)
}

func (memory *Memory[K, V]) remove(item *entry[K, V]) {
	if item == nil {
		return
	}
	memory.unlink(item)
	delete(memory.items, item.key)
}

func (memory *Memory[K, V]) unlink(item *entry[K, V]) {
	if item.previous != nil {
		item.previous.next = item.next
	} else {
		memory.head = item.next
	}
	if item.next != nil {
		item.next.previous = item.previous
	} else {
		memory.tail = item.previous
	}
	item.previous, item.next = nil, nil
}

func (memory *Memory[K, V]) observe(ctx context.Context, observation Observation) {
	for _, observer := range memory.observers {
		observer(ctx, observation)
	}
}

var _ Store[string, any] = (*Memory[string, any])(nil)
