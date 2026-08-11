// Package async provides bounded, lifecycle-owned asynchronous execution for
// generated Spice applications.
package async

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	// ErrClosed is returned when a task is submitted after shutdown starts.
	ErrClosed = errors.New("async executor is closed")
	// ErrPanicked identifies a task panic contained at the asynchronous
	// boundary.
	ErrPanicked = errors.New("async task panicked")
)

// Definition identifies one compiler-owned asynchronous task and its module.
type Definition struct {
	ID     string
	Module string
}

// Task executes with the executor's caller-owned lifetime context.
type Task func(context.Context) error

// Result describes one completed asynchronous task.
type Result struct {
	Definition Definition
	Duration   time.Duration
	Err        error
	Panicked   bool
}

// Observer receives task completion on the worker goroutine. It must not panic
// or block indefinitely.
type Observer func(context.Context, Result)

// Snapshot is a concurrency-safe executor view.
type Snapshot struct {
	Submitted uint64
	Running   int
	Completed uint64
	Failed    uint64
	Panicked  uint64
	Closed    bool
}

// PanicError reports a contained task panic without exposing the recovered
// value, which may contain application data.
type PanicError struct {
	Definition Definition
}

// Error describes the panicked task.
func (err *PanicError) Error() string {
	if err == nil {
		return ErrPanicked.Error()
	}
	return fmt.Sprintf("execute async task %q: %v", err.Definition.ID, ErrPanicked)
}

// Unwrap supports errors.Is(err, ErrPanicked).
func (err *PanicError) Unwrap() error {
	return ErrPanicked
}

// Executor admits at most one goroutine per concurrency slot. Submit applies
// backpressure instead of building a hidden queue.
type Executor struct {
	ctx       context.Context //nolint:containedctx // Executor explicitly retains the caller-owned task lifetime.
	cancel    context.CancelCauseFunc
	slots     chan struct{}
	closed    chan struct{}
	done      chan struct{}
	observers []Observer

	mu           sync.Mutex
	shutdownOnce sync.Once
	tasks        sync.WaitGroup
	results      []error
	result       error
	stats        Snapshot
}

// NewExecutor constructs an executor with a caller-owned lifetime context. It
// starts no goroutine until a task is accepted.
func NewExecutor(
	ctx context.Context,
	maxConcurrent int,
	observers ...Observer,
) (*Executor, error) {
	if ctx == nil {
		return nil, errors.New("construct async executor: context is nil")
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, fmt.Errorf("construct async executor: %w", cause)
	}
	if maxConcurrent < 1 {
		return nil, errors.New("construct async executor: max concurrency must be positive")
	}
	for index, observer := range observers {
		if observer == nil {
			return nil, fmt.Errorf("construct async executor: observer %d is nil", index)
		}
	}
	executionContext, cancel := context.WithCancelCause(ctx)
	return &Executor{
		ctx:       executionContext,
		cancel:    cancel,
		slots:     make(chan struct{}, maxConcurrent),
		closed:    make(chan struct{}),
		done:      make(chan struct{}),
		observers: append([]Observer(nil), observers...),
	}, nil
}

// Submit blocks until a concurrency slot is available or an admission,
// lifetime, or shutdown context ends. Once accepted, the task owns a bounded
// worker goroutine.
func (executor *Executor) Submit(
	admission context.Context,
	definition Definition,
	task Task,
) error {
	if admission == nil {
		return errors.New("submit async task: admission context is nil")
	}
	if executor == nil {
		return errors.New("submit async task: executor is nil")
	}
	if err := validateDefinition(definition); err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("submit async task %q: task is nil", definition.ID)
	}

	select {
	case executor.slots <- struct{}{}:
	case <-admission.Done():
		return fmt.Errorf("submit async task %q: %w", definition.ID, context.Cause(admission))
	case <-executor.ctx.Done():
		return fmt.Errorf("submit async task %q: %w", definition.ID, context.Cause(executor.ctx))
	case <-executor.closed:
		return fmt.Errorf("submit async task %q: %w", definition.ID, ErrClosed)
	}
	if cause := context.Cause(admission); cause != nil {
		<-executor.slots
		return fmt.Errorf("submit async task %q: %w", definition.ID, cause)
	}
	if cause := context.Cause(executor.ctx); cause != nil {
		<-executor.slots
		return fmt.Errorf("submit async task %q: %w", definition.ID, cause)
	}

	executor.mu.Lock()
	if executor.stats.Closed {
		executor.mu.Unlock()
		<-executor.slots
		return fmt.Errorf("submit async task %q: %w", definition.ID, ErrClosed)
	}
	index := len(executor.results)
	executor.results = append(executor.results, nil)
	executor.stats.Submitted++
	executor.stats.Running++
	executor.tasks.Add(1)
	executor.mu.Unlock()

	go executor.run(index, definition, task)
	return nil
}

// Shutdown stops admission and waits for accepted tasks. If ctx ends first,
// execution contexts are canceled and Shutdown returns without waiting for
// tasks that ignore cancellation. Concurrent calls share one terminal result.
func (executor *Executor) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("shutdown async executor: context is nil")
	}
	if executor == nil {
		return errors.New("shutdown async executor: executor is nil")
	}
	executor.beginShutdown()
	select {
	case <-executor.done:
		executor.mu.Lock()
		result := executor.result
		executor.mu.Unlock()
		return result
	case <-ctx.Done():
		cause := context.Cause(ctx)
		executor.cancel(cause)
		return fmt.Errorf("shutdown async executor: %w", cause)
	}
}

// Done closes after shutdown starts and every accepted task returns.
func (executor *Executor) Done() <-chan struct{} {
	if executor == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return executor.done
}

// Snapshot returns bounded executor statistics.
func (executor *Executor) Snapshot() Snapshot {
	if executor == nil {
		return Snapshot{Closed: true}
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.stats
}

func (executor *Executor) run(index int, definition Definition, task Task) {
	started := time.Now()
	err, panicked := invoke(executor.ctx, task)
	if panicked {
		err = &PanicError{Definition: definition}
	} else if err != nil {
		err = fmt.Errorf("execute async task %q: %w", definition.ID, err)
	}
	result := Result{
		Definition: definition,
		Duration:   time.Since(started),
		Err:        err,
		Panicked:   panicked,
	}

	executor.mu.Lock()
	executor.results[index] = err
	executor.stats.Running--
	executor.stats.Completed++
	if err != nil {
		executor.stats.Failed++
	}
	if panicked {
		executor.stats.Panicked++
	}
	executor.mu.Unlock()
	<-executor.slots
	for _, observer := range executor.observers {
		observer(executor.ctx, result)
	}
	executor.tasks.Done()
}

func (executor *Executor) beginShutdown() {
	executor.shutdownOnce.Do(func() {
		executor.mu.Lock()
		executor.stats.Closed = true
		close(executor.closed)
		executor.mu.Unlock()
		go func() {
			executor.tasks.Wait()
			executor.mu.Lock()
			executor.result = errors.Join(executor.results...)
			executor.mu.Unlock()
			executor.cancel(ErrClosed)
			close(executor.done)
		}()
	})
}

func validateDefinition(definition Definition) error {
	if definition.ID == "" {
		return errors.New("submit async task: task ID is required")
	}
	if definition.Module == "" {
		return fmt.Errorf("submit async task %q: module is required", definition.ID)
	}
	return nil
}

func invoke(ctx context.Context, task Task) (err error, panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	return task(ctx), false
}
