//go:build windows

package daemonprocess

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsManagedExitCode = 0x53504943 // "SPIC"
	windowsCleanupTimeout  = 5 * time.Second
)

// AdoptRootRegistry is a cross-platform generated-daemon seam. Windows child
// containment is inherited from the supervisor Job Object, so no additional
// daemon-side registry endpoint is required.
func AdoptRootRegistry() (RootRegistry, error) {
	return inactiveRootRegistry{}, nil
}

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

// startProcess publishes a process only after the suspended primary process is
// assigned to its kill-on-close job and its primary thread has been resumed.
// This removes the start-to-assignment escape window that exists with os/exec.
func startProcess(spec processSpec) (launchedProcess, error) {
	if err := validateWindowsProcessSpec(spec); err != nil {
		return nil, err
	}

	job, err := newWindowsJob()
	if err != nil {
		return nil, err
	}
	pipes, err := newWindowsProcessPipes()
	if err != nil {
		return nil, errors.Join(err, closeWindowsHandle(job))
	}
	published := false
	defer func() {
		if !published {
			_ = pipes.closeAll()        //nolint:errcheck // The primary setup error remains authoritative.
			_ = closeWindowsHandle(job) //nolint:errcheck // The primary setup error remains authoritative.
		}
	}()

	application, commandLine, directory, environment, err := windowsProcessParameters(spec)
	if err != nil {
		return nil, err
	}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	inherited := []windows.Handle{pipes.childInput, pipes.childOutput, pipes.childStderr}
	if err = attributes.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&inherited[0]), // #nosec G103 -- the fixed non-empty handle slice matches the Windows API buffer contract.
		uintptr(len(inherited))*unsafe.Sizeof(inherited[0]),
	); err != nil {
		return nil, err
	}
	startup := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})), // #nosec G115 -- static Windows structure size.
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  pipes.childInput,
			StdOutput: pipes.childOutput,
			StdErr:    pipes.childStderr,
		},
		ProcThreadAttributeList: attributes.List(),
	}
	information := windows.ProcessInformation{}
	flags := uint32(
		windows.CREATE_SUSPENDED |
			windows.CREATE_NEW_PROCESS_GROUP |
			windows.CREATE_NO_WINDOW |
			windows.CREATE_UNICODE_ENVIRONMENT |
			windows.EXTENDED_STARTUPINFO_PRESENT,
	)
	if err = windows.CreateProcess(
		application,
		commandLine,
		nil,
		nil,
		true,
		flags,
		&environment[0],
		directory,
		&startup.StartupInfo,
		&information,
	); err != nil {
		return nil, err
	}
	published = true
	return finishWindowsProcessStart(job, pipes, information, spec)
}

func finishWindowsProcessStart(
	job windows.Handle,
	pipes *windowsProcessPipes,
	information windows.ProcessInformation,
	spec processSpec,
) (launchedProcess, error) {
	var err error
	assigned := false
	if err = windows.AssignProcessToJobObject(job, information.Process); err == nil {
		assigned = true
	}
	childCloseErr := pipes.closeChildEnds()
	if err != nil || childCloseErr != nil {
		child, abortErr := abortWindowsProcess(
			job, information, assigned, pipes, spec, err, childCloseErr,
		)
		if child != nil {
			pipes.releaseAll()
		}
		return child, abortErr
	}
	previous, resumeErr := windows.ResumeThread(information.Thread)
	if resumeErr != nil || previous != 1 {
		if resumeErr == nil {
			resumeErr = fmt.Errorf("unexpected primary thread suspend count: %d", previous)
		}
		child, abortErr := abortWindowsProcess(job, information, true, pipes, spec, resumeErr, nil)
		if child != nil {
			pipes.releaseAll()
		}
		return child, abortErr
	}
	if err = closeWindowsHandle(information.Thread); err != nil {
		child, abortErr := abortWindowsProcess(job, information, true, pipes, spec, err, err)
		if child != nil {
			pipes.releaseAll()
		}
		return child, abortErr
	}
	information.Thread = 0

	process := &windowsProcess{
		process:    information.Process,
		job:        job,
		assigned:   true,
		input:      pipes.parentInput,
		stderr:     pipes.parentStderr,
		stderrDone: make(chan error, 1),
		waitDelay:  spec.waitDelay,
	}
	go process.copyStderr(spec.stderr)
	pipes.releaseParentEnds()
	return process, nil
}

func validateWindowsProcessSpec(spec processSpec) error {
	if spec.executable == "" || !filepath.IsAbs(spec.executable) ||
		filepath.Clean(spec.executable) != spec.executable ||
		spec.argument != daemonArgument || strings.IndexByte(spec.executable, 0) >= 0 ||
		spec.directory == "" || !filepath.IsAbs(spec.directory) ||
		filepath.Clean(spec.directory) != spec.directory || strings.IndexByte(spec.directory, 0) >= 0 ||
		spec.stderr == nil || spec.waitDelay <= 0 {
		return errors.New("managed daemon Windows process specification is invalid")
	}
	for _, value := range spec.environment {
		if strings.IndexByte(value, 0) >= 0 {
			return errors.New("managed daemon Windows environment is invalid")
		}
	}
	return nil
}

func newWindowsJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), // #nosec G103 -- Windows requires a pointer to the exact typed limits structure.
		uint32(unsafe.Sizeof(limits)),    // #nosec G115 -- static Windows structure size.
	)
	if err != nil {
		return 0, errors.Join(err, closeWindowsHandle(job))
	}
	return job, nil
}

type windowsProcessPipes struct {
	childInput   windows.Handle
	childOutput  windows.Handle
	childStderr  windows.Handle
	parentInput  *os.File
	parentStderr *os.File
}

func newWindowsProcessPipes() (*windowsProcessPipes, error) {
	security := windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})), // #nosec G115 -- static Windows structure size.
		InheritHandle: 1,
	}
	var childInput, parentInput windows.Handle
	if err := windows.CreatePipe(&childInput, &parentInput, &security, 0); err != nil {
		return nil, err
	}
	var parentStderr, childStderr windows.Handle
	if err := windows.CreatePipe(&parentStderr, &childStderr, &security, 0); err != nil {
		return nil, errors.Join(err, closeWindowsHandle(childInput), closeWindowsHandle(parentInput))
	}
	nulName, err := windows.UTF16PtrFromString("NUL")
	if err != nil {
		return nil, errors.Join(err, closeWindowsHandle(childInput), closeWindowsHandle(parentInput),
			closeWindowsHandle(parentStderr), closeWindowsHandle(childStderr))
	}
	childOutput, err := windows.CreateFile(
		nulName,
		windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		&security,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, errors.Join(err, closeWindowsHandle(childInput), closeWindowsHandle(parentInput),
			closeWindowsHandle(parentStderr), closeWindowsHandle(childStderr))
	}
	if err = windows.SetHandleInformation(parentInput, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return nil, errors.Join(err, closeWindowsHandle(childInput), closeWindowsHandle(parentInput),
			closeWindowsHandle(parentStderr), closeWindowsHandle(childStderr), closeWindowsHandle(childOutput))
	}
	if err = windows.SetHandleInformation(parentStderr, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return nil, errors.Join(err, closeWindowsHandle(childInput), closeWindowsHandle(parentInput),
			closeWindowsHandle(parentStderr), closeWindowsHandle(childStderr), closeWindowsHandle(childOutput))
	}
	return &windowsProcessPipes{
		childInput: childInput, childOutput: childOutput, childStderr: childStderr,
		parentInput:  os.NewFile(uintptr(parentInput), "managed-daemon-stdin"),
		parentStderr: os.NewFile(uintptr(parentStderr), "managed-daemon-stderr"),
	}, nil
}

func (pipes *windowsProcessPipes) closeChildEnds() error {
	if pipes == nil {
		return nil
	}
	err := errors.Join(
		closeWindowsHandle(pipes.childInput),
		closeWindowsHandle(pipes.childOutput),
		closeWindowsHandle(pipes.childStderr),
	)
	pipes.childInput = 0
	pipes.childOutput = 0
	pipes.childStderr = 0
	return err
}

func (pipes *windowsProcessPipes) closeAll() error {
	if pipes == nil {
		return nil
	}
	return errors.Join(pipes.closeChildEnds(), closeWindowsFile(pipes.parentInput), closeWindowsFile(pipes.parentStderr))
}

func (pipes *windowsProcessPipes) releaseParentEnds() {
	pipes.parentInput = nil
	pipes.parentStderr = nil
}

func (pipes *windowsProcessPipes) releaseAll() {
	pipes.childInput = 0
	pipes.childOutput = 0
	pipes.childStderr = 0
	pipes.releaseParentEnds()
}

func windowsProcessParameters(spec processSpec) (*uint16, *uint16, *uint16, []uint16, error) {
	application, err := windows.UTF16PtrFromString(spec.executable)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	commandLine, err := windows.UTF16PtrFromString(
		windows.ComposeCommandLine([]string{spec.executable, spec.argument}),
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	directory, err := windows.UTF16PtrFromString(spec.directory)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	environment, err := windowsEnvironmentBlock(spec.environment)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return application, commandLine, directory, environment, nil
}

func windowsEnvironmentBlock(environment []string) ([]uint16, error) {
	environment, err := normalizeWindowsEnvironment(environment)
	if err != nil {
		return nil, err
	}
	// A non-nil double-NUL block is intentional: an explicitly empty
	// environment must not inherit the launcher's ambient environment.
	block := make([]uint16, 0, 2)
	for _, value := range environment {
		block = append(block, utf16.Encode([]rune(value))...)
		block = append(block, 0)
	}
	block = append(block, 0)
	if len(block) == 1 {
		block = append(block, 0)
	}
	return block, nil
}

func normalizeWindowsEnvironment(environment []string) ([]string, error) {
	seen := make(map[string]struct{}, len(environment))
	normalized := make([]string, 0, len(environment))
	for _, value := range slices.Backward(environment) {
		key, ok := windowsEnvironmentKey(value)
		if !ok {
			return nil, errors.New("managed daemon Windows environment entry is invalid")
		}
		folded := strings.ToUpper(key)
		if _, duplicate := seen[folded]; duplicate {
			continue
		}
		seen[folded] = struct{}{}
		normalized = append(normalized, value)
	}
	for left, right := 0, len(normalized)-1; left < right; left, right = left+1, right-1 {
		normalized[left], normalized[right] = normalized[right], normalized[left]
	}
	sort.Slice(normalized, func(left, right int) bool {
		leftKey, _ := windowsEnvironmentKey(normalized[left])
		rightKey, _ := windowsEnvironmentKey(normalized[right])
		return strings.ToUpper(leftKey) < strings.ToUpper(rightKey)
	})
	return normalized, nil
}

func windowsEnvironmentKey(value string) (string, bool) {
	separator := strings.IndexByte(value, '=')
	if separator == 0 {
		next := strings.IndexByte(value[1:], '=')
		if next < 0 {
			return "", false
		}
		separator = next + 1
	}
	if separator < 0 {
		return "", false
	}
	return value[:separator], true
}

func abortWindowsProcess(
	job windows.Handle,
	information windows.ProcessInformation,
	assigned bool,
	pipes *windowsProcessPipes,
	spec processSpec,
	cause error,
	initialCleanupError error,
) (launchedProcess, error) {
	var terminationErr error
	if assigned {
		terminationErr = windows.TerminateJobObject(job, windowsManagedExitCode)
	} else {
		terminationErr = windows.TerminateProcess(information.Process, windowsManagedExitCode)
	}
	waitErr := waitWindowsHandle(information.Process, windowsCleanupTimeout)
	var jobDrainErr error
	if assigned {
		jobDrainErr = waitWindowsJobEmpty(job, windowsCleanupTimeout)
	}
	cleanupErr := errors.Join(initialCleanupError, terminationErr, waitErr, jobDrainErr)
	if cleanupErr != nil {
		return newFailedWindowsProcess(job, information, assigned, pipes, spec, cleanupErr, false, nil),
			errors.Join(cause, cleanupErr)
	}
	outcome := windowsProcessOutcome(information.Process)
	threadCloseErr := closeWindowsHandle(information.Thread)
	if threadCloseErr == nil {
		information.Thread = 0
	}
	jobCloseErr := closeWindowsHandle(job)
	if jobCloseErr == nil {
		job = 0
	}
	processCloseErr := closeWindowsHandle(information.Process)
	if processCloseErr == nil {
		information.Process = 0
	}
	pipeCloseErr := pipes.closeAll()
	cleanupErr = errors.Join(threadCloseErr, jobCloseErr, processCloseErr, pipeCloseErr)
	if cleanupErr != nil {
		return newFailedWindowsProcess(job, information, assigned, pipes, spec, cleanupErr, true, outcome),
			errors.Join(cause, cleanupErr)
	}
	return nil, cause
}

func newFailedWindowsProcess(
	job windows.Handle,
	information windows.ProcessInformation,
	assigned bool,
	pipes *windowsProcessPipes,
	spec processSpec,
	cleanupErr error,
	waitCompleted bool,
	outcome error,
) *windowsProcess {
	process := &windowsProcess{
		process: information.Process, job: job, thread: information.Thread, assigned: assigned,
		childEnds: []windows.Handle{pipes.childInput, pipes.childOutput, pipes.childStderr},
		input:     pipes.parentInput, stderr: pipes.parentStderr, stderrDone: make(chan error, 1),
		waitDelay: spec.waitDelay, failures: []error{cleanupErr},
		waitCompleted: waitCompleted, waitErr: outcome,
	}
	go process.copyStderr(spec.stderr)
	return process
}

func windowsProcessOutcome(handle windows.Handle) error {
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return err
	}
	if exitCode == 0 {
		return nil
	}
	return &windowsExitError{code: exitCode}
}

func (process *windowsProcess) copyStderr(destination io.Writer) {
	_, err := io.Copy(destination, process.stderr)
	process.stderrDone <- err
	close(process.stderrDone)
}

func (process *windowsProcess) Wait() error {
	process.waitOnce.Do(func() {
		if !process.waitCompleted {
			process.waitErr = waitWindowsHandle(process.process, 0)
			if process.waitErr == nil {
				process.waitErr = windowsProcessOutcome(process.process)
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
		closeErr := closeWindowsFile(process.stderr)
		process.stderr = nil
		// The read error caused by our bounded close is expected. The close error,
		// however, is a real loss of pipe ownership and remains observable.
		<-process.stderrDone
		return closeErr
	}
}

func (process *windowsProcess) CloseInput() error {
	process.inputOnce.Do(func() {
		process.inputErr = closeWindowsFile(process.input)
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
		stderrErr := closeWindowsFile(process.stderr)
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
			drainErr = waitWindowsJobEmpty(job, windowsCleanupTimeout)
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
			closeWindowsHandles(childEnds),
			closeWindowsHandle(thread),
			closeWindowsHandle(job),
			closeWindowsHandle(processHandle),
		)
	})
	return process.closeErr
}

func closeWindowsHandles(handles []windows.Handle) error {
	errorsByHandle := make([]error, 0, len(handles))
	for _, handle := range handles {
		errorsByHandle = append(errorsByHandle, closeWindowsHandle(handle))
	}
	return errors.Join(errorsByHandle...)
}

type windowsJobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func waitWindowsJobEmpty(job windows.Handle, timeout time.Duration) error {
	if job == 0 || job == windows.InvalidHandle {
		return errors.New("managed daemon Windows job handle is invalid")
	}
	deadline := time.Now().Add(timeout)
	for {
		accounting := windowsJobAccounting{}
		err := windows.QueryInformationJobObject(
			job,
			windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&accounting)), // #nosec G103 -- Windows requires a pointer to the exact typed accounting structure.
			uint32(unsafe.Sizeof(accounting)),    // #nosec G115 -- static Windows structure size.
			nil,
		)
		if err != nil {
			return err
		}
		if accounting.ActiveProcesses == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.New("managed daemon Windows job cleanup timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func (process *windowsProcess) recordFailure(err error) {
	if err == nil {
		return
	}
	process.mu.Lock()
	process.failures = append(process.failures, err)
	process.mu.Unlock()
}

type windowsExitError struct{ code uint32 }

func (failure *windowsExitError) Error() string {
	return fmt.Sprintf("managed daemon exited with status %d", failure.code)
}

func (failure *windowsExitError) ExitCode() uint32 {
	if failure == nil {
		return 0
	}
	return failure.code
}

func waitWindowsHandle(handle windows.Handle, timeout time.Duration) error {
	if handle == 0 || handle == windows.InvalidHandle {
		return errors.New("managed daemon Windows process handle is invalid")
	}
	milliseconds := uint32(windows.INFINITE)
	if timeout > 0 {
		milliseconds = uint32(min(timeout.Milliseconds(), int64(windows.INFINITE-1))) // #nosec G115 -- explicitly capped below uint32 maximum.
	}
	event, err := windows.WaitForSingleObject(handle, milliseconds)
	if err != nil {
		return err
	}
	if event == windows.WAIT_OBJECT_0 {
		return nil
	}
	if event == uint32(windows.WAIT_TIMEOUT) {
		return errors.New("managed daemon Windows process cleanup timed out")
	}
	return fmt.Errorf("unexpected managed daemon Windows wait result: %d", event)
}

func closeWindowsHandle(handle windows.Handle) error {
	if handle == 0 || handle == windows.InvalidHandle {
		return nil
	}
	return windows.CloseHandle(handle)
}

func closeWindowsFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

var _ launchedProcess = (*windowsProcess)(nil)
