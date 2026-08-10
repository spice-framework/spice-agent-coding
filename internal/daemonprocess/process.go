package daemonprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Process is one owned daemon candidate. Reaping begins immediately and occurs
// exactly once independently of Wait calls.
type Process struct {
	child       launchedProcess
	diagnostics *boundedBuffer
	done        chan struct{}
	graceful    time.Duration
	terminate   time.Duration

	mu       sync.Mutex
	result   error
	cleanup  error
	shutdown sync.Once
	beginErr error
}

func (process *Process) reap() {
	result := process.child.Wait()
	cleanup := (&ContainmentError{}).wrap(process.child.Close())
	process.mu.Lock()
	process.result = result
	process.cleanup = cleanup
	process.mu.Unlock()
	close(process.done)
}

func (process *Process) Done() <-chan struct{} {
	if process == nil {
		return nil
	}
	return process.done
}

func (process *Process) Result() error {
	if process == nil {
		return errors.New("managed daemon candidate is unavailable")
	}
	select {
	case <-process.done:
		process.mu.Lock()
		defer process.mu.Unlock()
		return process.result
	default:
		return errors.New("managed daemon candidate is still running")
	}
}

func (process *Process) BeginShutdown() error {
	if process == nil || process.done == nil {
		return errors.New("managed daemon candidate is unavailable")
	}
	process.shutdown.Do(func() {
		process.beginErr = (redactedError{}).wrap("request managed daemon shutdown", process.child.CloseInput())
		go process.escalate()
	})
	return process.beginErr
}

func (process *Process) escalate() {
	if process.waitDone(process.done, process.graceful) {
		return
	}
	if err := process.child.Terminate(); err != nil {
		_ = process.child.Kill() //nolint:errcheck // Platform Close retains the authoritative containment failure.
		return
	}
	if process.waitDone(process.done, process.terminate) {
		return
	}
	_ = process.child.Kill() //nolint:errcheck // Platform Close retains the authoritative containment failure.
}

func (process *Process) Wait(ctx context.Context) error {
	if process == nil || process.done == nil {
		return errors.New("managed daemon candidate is unavailable")
	}
	if ctx == nil {
		return errors.New("managed daemon wait context is required")
	}
	select {
	case <-process.done:
		return process.cleanupResult()
	default:
	}
	select {
	case <-process.done:
		return process.cleanupResult()
	case <-ctx.Done():
		select {
		case <-process.done:
			return process.cleanupResult()
		default:
			return (redactedError{}).wrap("wait for managed daemon process", context.Cause(ctx))
		}
	}
}

func (process *Process) cleanupResult() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.cleanup
}

func (process *Process) ProtectedStderr() []byte {
	if process == nil || process.diagnostics == nil {
		return nil
	}
	return process.diagnostics.Bytes()
}

func (*Process) waitDone(done <-chan struct{}, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (*Process) String() string           { return "daemonprocess.Process([REDACTED])" }
func (process *Process) GoString() string { return process.String() }
func (*Process) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "daemonprocess.Process([REDACTED])") //nolint:errcheck // fmt.Formatter cannot return an error.
}
func (process *Process) LogValue() slog.Value { return slog.StringValue(process.String()) }
