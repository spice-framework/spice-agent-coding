// Package schedule provides lifecycle-owned fixed-delay job execution for
// generated Spice applications.
package schedule

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

var (
	// ErrStarted identifies a duplicate scheduler start.
	ErrStarted = errors.New("scheduler is already started")
	// ErrClosed identifies a scheduler that has begun shutdown.
	ErrClosed = errors.New("scheduler is closed")
	// ErrPanicked identifies a contained scheduled-job panic.
	ErrPanicked = errors.New("scheduled job panicked")
)

// Definition identifies one compiler-owned job and its module.
type Definition struct {
	ID     string
	Module string
}

// Job is one serial fixed-delay schedule. ContinueOnError must be explicitly
// selected when a failed run is safe to repeat.
type Job struct {
	Definition      Definition
	InitialDelay    time.Duration
	Delay           time.Duration
	ContinueOnError bool
	Run             func(context.Context) error
}

// Waiter waits between runs. A nil Waiter uses a context-aware timer.
type Waiter func(context.Context, time.Duration) error

// Result describes one completed scheduled run.
type Result struct {
	Definition Definition
	Run        uint64
	Duration   time.Duration
	Err        error
	Panicked   bool
}

// Observer receives completed runs on the job goroutine.
type Observer func(context.Context, Result)

// Snapshot is a concurrency-safe scheduler view.
type Snapshot struct {
	Jobs     int
	Runs     uint64
	Running  int
	Failed   uint64
	Panicked uint64
	Started  bool
	Closed   bool
}

// PanicError reports a contained scheduled-job panic without exposing the
// recovered value.
type PanicError struct {
	Definition Definition
}

// Error describes the panicked job.
func (err *PanicError) Error() string {
	if err == nil {
		return ErrPanicked.Error()
	}
	return fmt.Sprintf("run scheduled job %q: %v", err.Definition.ID, ErrPanicked)
}

// Unwrap supports errors.Is(err, ErrPanicked).
func (err *PanicError) Unwrap() error {
	return ErrPanicked
}

// Scheduler owns immutable jobs and one goroutine per started job.
type Scheduler struct {
	taskContext context.Context //nolint:containedctx // Explicit caller-owned job lifetime.
	cancelTask  context.CancelCauseFunc
	loopContext context.Context //nolint:containedctx // Derived context stops future scheduled runs.
	cancelLoop  context.CancelCauseFunc
	jobs        []Job
	waiter      Waiter
	observers   []Observer
	done        chan struct{}

	mu       sync.Mutex
	wait     sync.WaitGroup
	results  []error
	result   error
	stats    Snapshot
	doneOnce sync.Once
}

// New constructs an inert scheduler. A nil waiter selects context-aware timers.
func New(
	lifetime context.Context,
	jobs []Job,
	waiter Waiter,
	observers ...Observer,
) (*Scheduler, error) {
	if lifetime == nil {
		return nil, errors.New("construct scheduler: lifetime context is nil")
	}
	if cause := context.Cause(lifetime); cause != nil {
		return nil, fmt.Errorf("construct scheduler: %w", cause)
	}
	frozen := append([]Job(nil), jobs...)
	seen := make(map[string]struct{}, len(frozen))
	for index, job := range frozen {
		if err := validateJob(index, job, seen); err != nil {
			return nil, err
		}
		seen[job.Definition.ID] = struct{}{}
	}
	slices.SortFunc(frozen, compareJobs)
	for index, observer := range observers {
		if observer == nil {
			return nil, fmt.Errorf("construct scheduler: observer %d is nil", index)
		}
	}
	taskContext, cancelTask := context.WithCancelCause(lifetime)
	loopContext, cancelLoop := context.WithCancelCause(lifetime)
	return &Scheduler{
		taskContext: taskContext,
		cancelTask:  cancelTask,
		loopContext: loopContext,
		cancelLoop:  cancelLoop,
		jobs:        frozen,
		waiter:      waiter,
		observers:   append([]Observer(nil), observers...),
		done:        make(chan struct{}),
		results:     make([]error, len(frozen)),
		stats:       Snapshot{Jobs: len(frozen)},
	}, nil
}

// Start launches each immutable job once. Its context bounds only the start
// transition; job execution uses the lifetime supplied to New.
func (scheduler *Scheduler) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("start scheduler: context is nil")
	}
	if scheduler == nil {
		return errors.New("start scheduler: scheduler is nil")
	}
	if cause := context.Cause(ctx); cause != nil {
		return fmt.Errorf("start scheduler: %w", cause)
	}
	if cause := context.Cause(scheduler.loopContext); cause != nil {
		return fmt.Errorf("start scheduler: %w", cause)
	}
	scheduler.mu.Lock()
	if scheduler.stats.Closed {
		scheduler.mu.Unlock()
		return fmt.Errorf("start scheduler: %w", ErrClosed)
	}
	if scheduler.stats.Started {
		scheduler.mu.Unlock()
		return fmt.Errorf("start scheduler: %w", ErrStarted)
	}
	scheduler.stats.Started = true
	scheduler.wait.Add(len(scheduler.jobs))
	scheduler.mu.Unlock()

	for index, job := range scheduler.jobs {
		go scheduler.runJob(index, job)
	}
	go scheduler.await()
	return nil
}

// Shutdown stops future runs and lets current runs drain. If ctx ends first,
// current task contexts are canceled and Shutdown returns without waiting for
// jobs that ignore cancellation.
func (scheduler *Scheduler) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("shutdown scheduler: context is nil")
	}
	if scheduler == nil {
		return errors.New("shutdown scheduler: scheduler is nil")
	}
	scheduler.mu.Lock()
	wasStarted := scheduler.stats.Started
	scheduler.stats.Closed = true
	scheduler.mu.Unlock()
	scheduler.cancelLoop(ErrClosed)
	if !wasStarted {
		scheduler.cancelTask(ErrClosed)
		scheduler.closeDone()
	}
	select {
	case <-scheduler.done:
		scheduler.mu.Lock()
		result := scheduler.result
		scheduler.mu.Unlock()
		return result
	case <-ctx.Done():
		cause := context.Cause(ctx)
		scheduler.cancelTask(cause)
		return fmt.Errorf("shutdown scheduler: %w", cause)
	}
}

// Done closes when every started job exits, or immediately when an unstarted
// scheduler shuts down.
func (scheduler *Scheduler) Done() <-chan struct{} {
	if scheduler == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return scheduler.done
}

// Snapshot returns aggregate scheduler state.
func (scheduler *Scheduler) Snapshot() Snapshot {
	if scheduler == nil {
		return Snapshot{Closed: true}
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return scheduler.stats
}

func (scheduler *Scheduler) runJob(index int, job Job) {
	defer scheduler.wait.Done()
	if err := scheduler.waitFor(job.InitialDelay); err != nil {
		return
	}
	var run uint64
	for context.Cause(scheduler.loopContext) == nil {
		run++
		started := time.Now()
		scheduler.mu.Lock()
		scheduler.stats.Running++
		scheduler.mu.Unlock()
		err, panicked := invoke(scheduler.taskContext, job.Run)
		if panicked {
			err = &PanicError{Definition: job.Definition}
		} else if err != nil {
			err = fmt.Errorf("run scheduled job %q: %w", job.Definition.ID, err)
		}
		result := Result{
			Definition: job.Definition,
			Run:        run,
			Duration:   time.Since(started),
			Err:        err,
			Panicked:   panicked,
		}
		scheduler.record(index, result)
		for _, observer := range scheduler.observers {
			observer(scheduler.taskContext, result)
		}
		if panicked || (err != nil && !job.ContinueOnError) {
			return
		}
		if context.Cause(scheduler.loopContext) != nil {
			return
		}
		if err := scheduler.waitFor(job.Delay); err != nil {
			return
		}
	}
}

func (scheduler *Scheduler) record(index int, result Result) {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	scheduler.stats.Running--
	scheduler.stats.Runs++
	if result.Err != nil {
		scheduler.stats.Failed++
	}
	if result.Panicked {
		scheduler.stats.Panicked++
	}
	if result.Err != nil && (result.Panicked || !scheduler.jobs[index].ContinueOnError) {
		scheduler.results[index] = result.Err
	}
}

func (scheduler *Scheduler) waitFor(delay time.Duration) error {
	if delay == 0 {
		return context.Cause(scheduler.loopContext)
	}
	if scheduler.waiter != nil {
		return scheduler.waiter(scheduler.loopContext, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-scheduler.loopContext.Done():
		return context.Cause(scheduler.loopContext)
	}
}

func (scheduler *Scheduler) await() {
	scheduler.wait.Wait()
	scheduler.mu.Lock()
	scheduler.result = errors.Join(scheduler.results...)
	scheduler.mu.Unlock()
	scheduler.cancelTask(ErrClosed)
	scheduler.closeDone()
}

func (scheduler *Scheduler) closeDone() {
	scheduler.doneOnce.Do(func() {
		close(scheduler.done)
	})
}

func validateJob(index int, job Job, seen map[string]struct{}) error {
	if job.Definition.ID == "" {
		return fmt.Errorf("construct scheduler: job %d has no ID", index)
	}
	if job.Definition.Module == "" {
		return fmt.Errorf(
			"construct scheduler: job %q has no module",
			job.Definition.ID,
		)
	}
	if job.Delay <= 0 {
		return fmt.Errorf("construct scheduler: job %q delay must be positive", job.Definition.ID)
	}
	if job.InitialDelay < 0 {
		return fmt.Errorf(
			"construct scheduler: job %q initial delay must not be negative",
			job.Definition.ID,
		)
	}
	if job.Run == nil {
		return fmt.Errorf("construct scheduler: job %q has no function", job.Definition.ID)
	}
	if _, duplicate := seen[job.Definition.ID]; duplicate {
		return fmt.Errorf(
			"construct scheduler: duplicate job ID %q",
			job.Definition.ID,
		)
	}
	return nil
}

func compareJobs(left, right Job) int {
	if compared := cmp.Compare(left.Definition.Module, right.Definition.Module); compared != 0 {
		return compared
	}
	return cmp.Compare(left.Definition.ID, right.Definition.ID)
}

func invoke(ctx context.Context, run func(context.Context) error) (err error, panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	return run(ctx), false
}
