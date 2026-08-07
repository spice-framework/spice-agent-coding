package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
)

const maximumCapturedStderrBytes = 64 << 10

var (
	errReadinessInvalid      = errors.New("runtime plugin stdout readiness record is invalid")
	errReadinessContaminated = errors.New("runtime plugin stdout is contaminated after readiness")
	errReadinessStreamClosed = errors.New("runtime plugin stdout closed before readiness")
)

// readinessSink recognizes the one permitted stdout record without retaining
// process output. Write always accepts the complete input so an invalid or
// noisy child cannot block while the process owner is stopping it.
type readinessSink struct {
	mu         sync.Mutex
	transition chan struct{}
	failed     chan struct{}
	matched    int
	ready      bool
	closed     bool
	failure    error
}

func newReadinessSink() *readinessSink {
	return &readinessSink{
		transition: make(chan struct{}),
		failed:     make(chan struct{}),
	}
}

// Write consumes stdout without ever returning a process-visible error. The
// first mismatch is retained as bounded, redacted state and all later bytes
// are discarded.
func (sink *readinessSink) Write(content []byte) (int, error) {
	written := len(content)
	sink.mu.Lock()
	defer sink.mu.Unlock()

	if sink.failure != nil || len(content) == 0 {
		return written, nil
	}
	if sink.closed {
		sink.failLocked(errReadinessContaminated)
		return written, nil
	}

	record := pluginv1.ReadinessRecord
	for _, value := range content {
		if sink.ready {
			sink.failLocked(errReadinessContaminated)
			break
		}
		if sink.matched >= len(record) || value != record[sink.matched] {
			sink.failLocked(errReadinessInvalid)
			break
		}
		sink.matched++
		if sink.matched == len(record) {
			sink.ready = true
			close(sink.transition)
		}
	}
	return written, nil
}

// Close marks the stdout stream complete. Closing after exact readiness is
// successful; closing a partial or empty stream is a safe startup failure.
func (sink *readinessSink) Close() error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.closed {
		return nil
	}
	sink.closed = true
	if !sink.ready && sink.failure == nil {
		sink.failLocked(errReadinessStreamClosed)
	}
	return nil
}

// wait waits until the exact readiness record, a failure, or cancellation.
// Callers must continue observing failed after readiness because any later
// stdout byte contaminates the process protocol.
func (sink *readinessSink) wait(ctx context.Context) error {
	if result, complete := sink.result(); complete {
		return result
	}
	select {
	case <-sink.transition:
		result, _ := sink.result()
		return result
	case <-ctx.Done():
		// Prefer an already-observed process transition over a simultaneously
		// delivered cancellation so readiness has deterministic ownership.
		if result, complete := sink.result(); complete {
			return result
		}
		return ctx.Err()
	}
}

// isReady reports whether the exact record has been observed. A ready process
// can subsequently fail if it writes another stdout byte.
func (sink *readinessSink) isReady() bool {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.ready
}

// err returns the first static, redacted stdout contract failure.
func (sink *readinessSink) err() error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.failure
}

// failureSignal closes on the first stdout contract failure, including one
// observed after successful startup.
func (sink *readinessSink) failureSignal() <-chan struct{} {
	return sink.failed
}

func (sink *readinessSink) result() (error, bool) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.failure != nil {
		return sink.failure, true
	}
	return nil, sink.ready
}

func (sink *readinessSink) failLocked(failure error) {
	if sink.failure != nil {
		return
	}
	sink.failure = failure
	close(sink.failed)
	if !sink.ready {
		close(sink.transition)
	}
}

// stderrSink drains all process stderr while retaining only a fixed-size
// prefix for internal diagnostics. Its formatting and JSON representation
// expose counts only, so child-controlled bytes cannot leak accidentally.
type stderrSink struct {
	mu        sync.Mutex
	captured  []byte
	total     uint64
	truncated bool
}

func newStderrSink() *stderrSink {
	return &stderrSink{captured: make([]byte, 0, maximumCapturedStderrBytes)}
}

// Write drains all input and stores at most maximumCapturedStderrBytes.
func (sink *stderrSink) Write(content []byte) (int, error) {
	written := len(content)
	sink.mu.Lock()
	defer sink.mu.Unlock()

	sink.total = saturatingAdd(sink.total, uint64(written))
	remaining := maximumCapturedStderrBytes - len(sink.captured)
	if remaining > 0 {
		captured := min(remaining, written)
		sink.captured = append(sink.captured, content[:captured]...)
	}
	if written > remaining {
		sink.truncated = true
	}
	return written, nil
}

func (sink *stderrSink) snapshot() stderrSnapshot {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return stderrSnapshot{
		captured:  append([]byte(nil), sink.captured...),
		total:     sink.total,
		truncated: sink.truncated,
	}
}

// clear destroys the bounded plugin-controlled prefix after process
// containment. Counts are reset as well so a closed candidate retains no
// child-output state.
func (sink *stderrSink) clear() {
	if sink == nil {
		return
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	clear(sink.captured)
	sink.captured = nil
	sink.total = 0
	sink.truncated = false
}

func (sink *stderrSink) String() string { return sink.snapshot().String() }

func (sink *stderrSink) GoString() string { return sink.snapshot().GoString() }

func (sink *stderrSink) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, sink.String())
}

func (sink *stderrSink) MarshalJSON() ([]byte, error) {
	return sink.snapshot().MarshalJSON()
}

// stderrSnapshot owns a stable copy of the bounded internal stderr prefix.
// The bytes method deliberately remains package-private; all ordinary
// formatting and serialization is metadata-only.
type stderrSnapshot struct {
	captured  []byte
	total     uint64
	truncated bool
}

func (snapshot stderrSnapshot) bytes() []byte {
	return append([]byte(nil), snapshot.captured...)
}

func (snapshot stderrSnapshot) totalBytes() uint64 { return snapshot.total }

func (snapshot stderrSnapshot) wasTruncated() bool { return snapshot.truncated }

func (snapshot stderrSnapshot) String() string {
	return fmt.Sprintf(
		"runtime plugin stderr metadata (captured=%d total=%d truncated=%t)",
		len(snapshot.captured), snapshot.total, snapshot.truncated,
	)
}

func (snapshot stderrSnapshot) GoString() string { return snapshot.String() }

func (snapshot stderrSnapshot) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, snapshot.String())
}

func (snapshot stderrSnapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Captured  int    `json:"captured_bytes"`
		Total     uint64 `json:"total_bytes"`
		Truncated bool   `json:"truncated"`
	}{
		Captured:  len(snapshot.captured),
		Total:     snapshot.total,
		Truncated: snapshot.truncated,
	})
}

func saturatingAdd(left, right uint64) uint64 {
	if math.MaxUint64-left < right {
		return math.MaxUint64
	}
	return left + right
}
