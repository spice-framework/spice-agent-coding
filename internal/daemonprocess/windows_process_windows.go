//go:build windows

package daemonprocess

import (
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const (
	windowsManagedExitCode = 0x53504943 // "SPIC"
	windowsCleanupTimeout  = 5 * time.Second
)

type windowsProcess struct {
	mu            sync.Mutex
	process       windows.Handle
	job           windows.Handle
	thread        windows.Handle
	assigned      bool
	childEnds     []windows.Handle
	input         *os.File
	stderr        *os.File
	stderrDone    chan error
	waitDelay     time.Duration
	failures      []error
	closed        bool
	inputOnce     sync.Once
	inputErr      error
	waitOnce      sync.Once
	waitCompleted bool
	waitErr       error
	closeOnce     sync.Once
	closeErr      error
}

func (process *windowsProcess) copyStderr(destination io.Writer) {
	_, err := io.Copy(destination, process.stderr)
	process.stderrDone <- err
	close(process.stderrDone)
}

func (process *windowsProcess) Wait() error {
	process.waitOnce.Do(func() {
		if !process.waitCompleted {
			process.waitErr = (windowsHandleOwner{}).wait(process.process, 0)
			if process.waitErr == nil {
				process.waitErr = (processFactory{}).outcome(process.process)
			}
			process.waitCompleted = true
		}
		process.recordFailure(process.finishStderr())
	})
	return process.waitErr
}

func (process *windowsProcess) finishStderr() error {
	timer := time.NewTimer(process.waitDelay)
	defer timer.Stop()
	select {
	case err := <-process.stderrDone:
		return err
	case <-timer.C:
		closeErr := (windowsHandleOwner{}).closeFile(process.stderr)
		process.stderr = nil
		<-process.stderrDone
		return closeErr
	}
}

func (process *windowsProcess) CloseInput() error {
	process.inputOnce.Do(func() {
		process.inputErr = (windowsHandleOwner{}).closeFile(process.input)
		process.input = nil
		process.recordFailure(process.inputErr)
	})
	return process.inputErr
}

func (process *windowsProcess) Terminate() error {
	return process.terminate(windowsManagedExitCode)
}

func (process *windowsProcess) Kill() error {
	return process.terminate(windowsManagedExitCode + 1)
}

func (process *windowsProcess) terminate(exitCode uint32) error {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.closed || process.job == 0 {
		if process.closed || process.process == 0 || process.assigned {
			return nil
		}
		err := windows.TerminateProcess(process.process, exitCode)
		if err != nil {
			process.failures = append(process.failures, err)
		}
		return err
	}
	var err error
	if process.assigned {
		err = windows.TerminateJobObject(process.job, exitCode)
	} else {
		err = windows.TerminateProcess(process.process, exitCode)
	}
	if err != nil {
		process.failures = append(process.failures, err)
	}
	return err
}

func (process *windowsProcess) Close() error {
	process.closeOnce.Do(func() {
		inputErr := process.CloseInput()
		stderrErr := (windowsHandleOwner{}).closeFile(process.stderr)
		process.stderr = nil

		process.mu.Lock()
		process.closed = true
		job := process.job
		processHandle := process.process
		thread := process.thread
		childEnds := append([]windows.Handle(nil), process.childEnds...)
		assigned := process.assigned
		process.job = 0
		process.process = 0
		process.thread = 0
		process.childEnds = nil
		var terminationErr, drainErr error
		if assigned && job != 0 {
			terminationErr = windows.TerminateJobObject(job, windowsManagedExitCode+2)
			drainErr = (windowsJob{}).waitEmpty(job, windowsCleanupTimeout)
		} else if processHandle != 0 && !process.waitCompleted {
			terminationErr = windows.TerminateProcess(processHandle, windowsManagedExitCode+2)
		}
		failures := append([]error(nil), process.failures...)
		process.mu.Unlock()

		process.closeErr = errors.Join(
			inputErr,
			stderrErr,
			errors.Join(failures...),
			terminationErr,
			drainErr,
			(windowsHandleOwner{}).closeAll(childEnds),
			(windowsHandleOwner{}).close(thread),
			(windowsHandleOwner{}).close(job),
			(windowsHandleOwner{}).close(processHandle),
		)
	})
	return process.closeErr
}

func (process *windowsProcess) recordFailure(err error) {
	if err == nil {
		return
	}
	process.mu.Lock()
	process.failures = append(process.failures, err)
	process.mu.Unlock()
}
