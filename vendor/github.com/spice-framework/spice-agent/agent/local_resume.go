package agent

import (
	"errors"
	"fmt"
	"math"
)

const runStatusResumePending LifecycleStatus = "resume-pending"

// PreparedLocalResume is an inert, context-free reservation for resuming one
// locally suspended run. Preparation does not unblock execution, publish an
// event, invoke a provider or tool, or transfer the run to another process.
// The caller must finish every preparation with Commit, Abort, or Close.
//
// Cancellation of the run or shutdown of its engine may occur while the
// decision is pending. The cancellation is latched at the suspended boundary:
// Commit or Abort releases finalization, and no post-boundary work can start.
type PreparedLocalResume struct {
	run          *Run
	runID        string
	nextSequence uint64
	decision     chan struct{}
	state        preparedExecutionState
}

// PrepareLocalResume exclusively reserves the current suspended boundary for
// a later local Commit or Abort. It performs no I/O and takes no context.
func (run *Run) PrepareLocalResume() (*PreparedLocalResume, error) {
	if run == nil {
		return nil, errors.New("agent run is nil")
	}
	run.stateMu.Lock()
	defer run.stateMu.Unlock()
	if run.ctx != nil &&
		run.status != LifecycleCompleted &&
		run.status != LifecycleFailed &&
		run.status != LifecycleCancelled {
		if err := run.ctx.Err(); err != nil {
			return nil, err
		}
	}
	if run.status != LifecycleSuspended || run.resumeSignal == nil || run.localResume != nil || len(run.activeInteractions) != 0 {
		return nil, &UnsafeSnapshotError{Status: run.status, ActiveInteractions: len(run.activeInteractions)}
	}
	if run.lastSequence == math.MaxUint64 {
		return nil, errors.New("agent suspended run event sequence is exhausted")
	}
	prepared := &PreparedLocalResume{
		run:          run,
		runID:        run.id,
		nextSequence: run.lastSequence + 1,
		decision:     make(chan struct{}),
		state:        preparedExecutionReady,
	}
	run.localResume = prepared
	run.status = runStatusResumePending
	return prepared, nil
}

// RunID returns the immutable identity of the reserved run.
func (prepared *PreparedLocalResume) RunID() string {
	if prepared == nil {
		return ""
	}
	return prepared.runID
}

// NextSequence returns the first event sequence that may be committed after
// the reserved boundary.
func (prepared *PreparedLocalResume) NextSequence() uint64 {
	if prepared == nil {
		return 0
	}
	return prepared.nextSequence
}

// Commit makes the suspended boundary non-exportable and releases execution.
// If cancellation was already requested, execution proceeds directly to
// terminal cancellation without starting post-boundary work.
func (prepared *PreparedLocalResume) Commit() error {
	if prepared == nil || prepared.run == nil {
		return errors.New("agent prepared local resume is nil")
	}
	run := prepared.run
	run.stateMu.Lock()
	defer run.stateMu.Unlock()
	switch prepared.state {
	case preparedExecutionCommitted:
		return ErrPreparedExecutionCommitted
	case preparedExecutionAborted:
		return ErrPreparedExecutionAborted
	}
	if run.localResume != prepared || run.status != runStatusResumePending || run.resumeSignal == nil {
		return fmt.Errorf("agent prepared local resume lost its suspended boundary")
	}
	resumeSignal := run.resumeSignal
	prepared.state = preparedExecutionCommitted
	run.localResume = nil
	run.status = runStatusRunning
	run.resumeSignal = nil
	close(prepared.decision)
	close(resumeSignal)
	return nil
}

// Abort releases the reservation. A live run returns to its byte-identical
// suspended boundary. If run cancellation is already pending, Abort instead
// releases the execution goroutine so terminal finalization can complete.
func (prepared *PreparedLocalResume) Abort() error {
	if prepared == nil || prepared.run == nil {
		return errors.New("agent prepared local resume is nil")
	}
	run := prepared.run
	run.stateMu.Lock()
	defer run.stateMu.Unlock()
	switch prepared.state {
	case preparedExecutionCommitted:
		return ErrPreparedExecutionCommitted
	case preparedExecutionAborted:
		return nil
	}
	if run.localResume != prepared || run.status != runStatusResumePending || run.resumeSignal == nil {
		return fmt.Errorf("agent prepared local resume lost its suspended boundary")
	}
	prepared.state = preparedExecutionAborted
	run.localResume = nil
	if run.ctx.Err() != nil {
		run.status = runStatusFinishing
	} else {
		run.status = LifecycleSuspended
	}
	close(prepared.decision)
	return nil
}

// Close is the standard cleanup form for an uncommitted local resume.
func (prepared *PreparedLocalResume) Close() error { return prepared.Abort() }
