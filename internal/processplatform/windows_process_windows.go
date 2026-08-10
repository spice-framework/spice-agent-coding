//go:build windows

package processplatform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	agentprocess "github.com/spice-framework/spice-agent/process"
	"golang.org/x/sys/windows"
)

const (
	windowsStopExitCode    = 0x53504350 // "SPCP"
	windowsKillExitCode    = windowsStopExitCode + 1
	windowsJobPollInterval = 2 * time.Millisecond
)

type windowsProcess struct {
	mu       sync.Mutex
	process  windows.Handle
	job      windows.Handle
	thread   windows.Handle
	assigned bool
	input    *os.File
	output   *os.File
	stderr   *os.File
	done     chan struct{}
	joined   chan struct{}

	inputOnce  sync.Once
	inputErr   error
	inputDone  chan error
	copyDone   chan error
	outcome    agentprocess.Outcome
	resultErr  error
	cleanupErr error
	stopSent   bool
	killSent   bool
	closed     bool
}

func (*windowsProcess) start(
	_ context.Context,
	spec agentprocess.Spec,
	registrar ChildRegistrar,
) (agentprocess.Process, error) {
	prototype := &windowsProcess{}
	job, err := prototype.newPlatformJob()
	if err != nil {
		return nil, err
	}
	pipes := &platformPipes{}
	err = pipes.initialize()
	if err != nil {
		return nil, errors.Join(err, (windowsHandleSet{}).closeHandle(job))
	}
	ownedByChild := false
	defer func() {
		if !ownedByChild {
			_ = pipes.closeAll()                      //nolint:errcheck // The launch failure remains authoritative.
			_ = (windowsHandleSet{}).closeHandle(job) //nolint:errcheck // The launch failure remains authoritative.
		}
	}()

	information, err := prototype.createSuspendedProcess(spec, pipes)
	if err != nil {
		return nil, err
	}
	ownedByChild = true
	if err = windows.AssignProcessToJobObject(job, information.Process); err != nil {
		return prototype.abortLaunch(job, pipes, information, false, spec, err, nil)
	}
	if err = pipes.closeChildEnds(); err != nil {
		return prototype.abortLaunch(job, pipes, information, true, spec, err, err)
	}
	previous, resumeErr := windows.ResumeThread(information.Thread)
	if resumeErr != nil || previous != 1 {
		if resumeErr == nil {
			resumeErr = fmt.Errorf("unexpected primary thread suspend count: %d", previous)
		}
		return prototype.abortLaunch(job, pipes, information, true, spec, resumeErr, nil)
	}
	if err = (windowsHandleSet{}).closeHandle(information.Thread); err != nil {
		return prototype.abortLaunch(job, pipes, information, true, spec, err, err)
	}

	owned := &windowsProcess{
		process: information.Process, job: job, assigned: true,
		input: pipes.parentInput, output: pipes.parentOutput, stderr: pipes.parentStderr,
		done: make(chan struct{}), joined: make(chan struct{}),
		inputDone: make(chan error, 1), copyDone: make(chan error, 2),
	}
	pipes.releaseParentEnds()
	registered, findErr := os.FindProcess(int(information.ProcessId))
	var registrationErr, releaseErr error
	if findErr == nil {
		registrationErr = registrar.Register(registered)
		releaseErr = registered.Release()
	}
	owned.startCopies(spec)
	go owned.reap()
	if findErr != nil || registrationErr != nil || releaseErr != nil {
		return owned, errors.Join(findErr, registrationErr, releaseErr)
	}
	return owned, nil
}

func (owned *windowsProcess) createSuspendedProcess(
	spec agentprocess.Spec,
	pipes *platformPipes,
) (windows.ProcessInformation, error) {
	application, commandLine, directory, environment, err := owned.parameters(spec)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	defer attributes.Delete()
	inherited := []windows.Handle{pipes.childInput, pipes.childOutput, pipes.childStderr}
	if err = attributes.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&inherited[0]), // #nosec G103 -- exact fixed handle-list buffer required by Windows.
		uintptr(len(inherited))*unsafe.Sizeof(inherited[0]),
	); err != nil {
		return windows.ProcessInformation{}, err
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{
		Cb:       uint32(unsafe.Sizeof(windows.StartupInfoEx{})), // #nosec G115 -- static Windows structure size.
		Flags:    windows.STARTF_USESTDHANDLES,
		StdInput: pipes.childInput, StdOutput: pipes.childOutput, StdErr: pipes.childStderr,
	}, ProcThreadAttributeList: attributes.List()}
	information := windows.ProcessInformation{}
	flags := uint32(
		windows.CREATE_SUSPENDED |
			windows.CREATE_NEW_PROCESS_GROUP |
			windows.CREATE_NO_WINDOW |
			windows.CREATE_UNICODE_ENVIRONMENT |
			windows.EXTENDED_STARTUPINFO_PRESENT,
	)
	err = windows.CreateProcess(
		application, commandLine, nil, nil, true, flags, &environment[0], directory,
		&startup.StartupInfo, &information,
	)
	return information, err
}

func (*windowsProcess) abortLaunch(
	job windows.Handle,
	pipes *platformPipes,
	information windows.ProcessInformation,
	assigned bool,
	spec agentprocess.Spec,
	cause error,
	initialCleanupErr error,
) (agentprocess.Process, error) {
	childCloseErr := pipes.closeChildEnds()
	var terminateErr error
	if assigned {
		terminateErr = windows.TerminateJobObject(job, windowsKillExitCode)
	} else {
		terminateErr = windows.TerminateProcess(information.Process, windowsKillExitCode)
	}
	cleanupErr := errors.Join(initialCleanupErr, childCloseErr)
	// Ownership always transfers after CreateProcess. Even when the immediate
	// abort succeeds, the caller receives the candidate and joins root outcome,
	// stream drains, Job emptiness, and every still-live handle exactly once.
	owned := &windowsProcess{
		process: information.Process, job: job, thread: information.Thread, assigned: assigned,
		input: pipes.parentInput, output: pipes.parentOutput, stderr: pipes.parentStderr,
		done: make(chan struct{}), joined: make(chan struct{}),
		inputDone: make(chan error, 1), copyDone: make(chan error, 2),
		cleanupErr: cleanupErr, killSent: terminateErr == nil,
	}
	pipes.releaseParentEnds()
	owned.startCopies(spec)
	go owned.reap()
	return owned, errors.Join(cause, childCloseErr, terminateErr)
}

func (owned *windowsProcess) startCopies(spec agentprocess.Spec) {
	input := owned.input
	go owned.copyInput(spec.Stdin(), input)
	go owned.copyOutput(spec.Stdout(), owned.output)
	go owned.copyOutput(spec.Stderr(), owned.stderr)
}

func (owned *windowsProcess) copyInput(source io.Reader, destination *os.File) {
	_, copyErr := io.Copy(destination, source)
	_ = owned.closeInput() //nolint:errcheck // Wait reports the authoritative cleanup result.
	if errors.Is(copyErr, os.ErrClosed) {
		copyErr = nil
	}
	owned.inputDone <- copyErr
}

func (owned *windowsProcess) copyOutput(destination io.Writer, source *os.File) {
	_, copyErr := io.Copy(destination, source)
	closeErr := (windowsHandleSet{}).closeFile(source)
	if errors.Is(copyErr, os.ErrClosed) {
		copyErr = nil
	}
	if errors.Is(closeErr, os.ErrClosed) {
		closeErr = nil
	}
	owned.copyDone <- errors.Join(copyErr, closeErr)
}

func (owned *windowsProcess) reap() {
	waitErr := (windowsJobMonitor{}).waitHandle(owned.process, 0)
	var outcome agentprocess.Outcome
	var resultErr error
	if waitErr == nil {
		owned.mu.Lock()
		terminated := owned.stopSent || owned.killSent
		owned.mu.Unlock()
		outcome, resultErr = owned.deriveOutcome(owned.process, terminated)
	} else {
		resultErr = agentprocess.NewFailure(agentprocess.OperationResult, waitErr)
	}
	owned.mu.Lock()
	owned.outcome = outcome
	owned.resultErr = resultErr
	owned.mu.Unlock()
	close(owned.done)
	owned.cleanup()
}

func (*windowsProcess) deriveOutcome(handle windows.Handle, terminated bool) (agentprocess.Outcome, error) {
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return agentprocess.Outcome{}, agentprocess.NewFailure(agentprocess.OperationResult, err)
	}
	if terminated && (code == windowsStopExitCode || code == windowsKillExitCode) {
		return agentprocess.NewSignaledOutcome(), nil
	}
	outcome, err := agentprocess.NewExitedOutcome(int64(code))
	if err != nil {
		return agentprocess.Outcome{}, agentprocess.NewFailure(agentprocess.OperationResult, err)
	}
	return outcome, nil
}

func (owned *windowsProcess) cleanup() {
	var jobErr error
	if owned.assigned {
		jobErr = (windowsJobMonitor{}).waitEmpty(owned.job)
	}
	inputErr := owned.closeInput()
	inputCopyErr := <-owned.inputDone
	outputErr := <-owned.copyDone
	stderrErr := <-owned.copyDone

	owned.mu.Lock()
	processHandle, jobHandle, threadHandle := owned.process, owned.job, owned.thread
	owned.process, owned.job, owned.thread = 0, 0, 0
	owned.output, owned.stderr = nil, nil
	owned.closed = true
	owned.mu.Unlock()
	handleErr := errors.Join(
		(windowsHandleSet{}).closeHandle(threadHandle),
		(windowsHandleSet{}).closeHandle(processHandle),
		(windowsHandleSet{}).closeHandle(jobHandle),
	)

	owned.mu.Lock()
	owned.cleanupErr = errors.Join(
		owned.cleanupErr, jobErr, inputErr, inputCopyErr, outputErr, stderrErr, handleErr,
	)
	owned.mu.Unlock()
	close(owned.joined)
}

func (owned *windowsProcess) closeInput() error {
	owned.inputOnce.Do(func() {
		owned.inputErr = (windowsHandleSet{}).closeFile(owned.input)
		if errors.Is(owned.inputErr, os.ErrClosed) {
			owned.inputErr = nil
		}
		owned.input = nil
	})
	return owned.inputErr
}

func (owned *windowsProcess) Done() <-chan struct{} {
	if owned == nil {
		return nil
	}
	return owned.done
}

func (owned *windowsProcess) Result() (agentprocess.Outcome, error) {
	if owned == nil || owned.done == nil {
		return agentprocess.Outcome{}, agentprocess.NewFailure(agentprocess.OperationResult, errors.New("owned process is unavailable"))
	}
	select {
	case <-owned.done:
		owned.mu.Lock()
		defer owned.mu.Unlock()
		return owned.outcome, owned.resultErr
	default:
		return agentprocess.Outcome{}, agentprocess.NewFailure(agentprocess.OperationResult, errors.New("root process is still running"))
	}
}

func (owned *windowsProcess) RequestStop(ctx context.Context) error {
	return owned.terminate(ctx, windowsStopExitCode, agentprocess.OperationRequestStop, false)
}

func (owned *windowsProcess) ForceKill(ctx context.Context) error {
	return owned.terminate(ctx, windowsKillExitCode, agentprocess.OperationForceKill, true)
}

func (owned *windowsProcess) terminate(
	ctx context.Context,
	code uint32,
	operation agentprocess.Operation,
	force bool,
) error {
	if err := (processOperationPolicy{}).context(ctx, operation); err != nil {
		return err
	}
	if owned == nil {
		return agentprocess.NewFailure(operation, errors.New("owned process is unavailable"))
	}
	owned.mu.Lock()
	defer owned.mu.Unlock()
	if owned.closed || (force && owned.killSent) || (!force && owned.stopSent) {
		return nil
	}
	var err error
	if owned.assigned {
		err = windows.TerminateJobObject(owned.job, code)
	} else {
		err = windows.TerminateProcess(owned.process, code)
	}
	if err != nil {
		return agentprocess.NewFailure(operation, err)
	}
	if force {
		owned.killSent = true
	} else {
		owned.stopSent = true
	}
	return nil
}

func (owned *windowsProcess) Wait(ctx context.Context) error {
	if err := (processOperationPolicy{}).context(ctx, agentprocess.OperationWait); err != nil {
		return err
	}
	if owned == nil || owned.joined == nil {
		return agentprocess.NewFailure(agentprocess.OperationWait, errors.New("owned process is unavailable"))
	}
	select {
	case <-owned.joined:
		owned.mu.Lock()
		defer owned.mu.Unlock()
		return (processOperationPolicy{}).terminalContainmentFailure(owned.cleanupErr)
	case <-ctx.Done():
		return agentprocess.NewFailure(agentprocess.OperationWait, context.Cause(ctx))
	}
}

func (owned *windowsProcess) parameters(spec agentprocess.Spec) (*uint16, *uint16, *uint16, []uint16, error) {
	application, err := windows.UTF16PtrFromString(spec.Executable())
	if err != nil {
		return nil, nil, nil, nil, err
	}
	arguments := append([]string{spec.Executable()}, spec.Arguments()...)
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(arguments))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	directory, err := windows.UTF16PtrFromString(spec.WorkingDirectory())
	if err != nil {
		return nil, nil, nil, nil, err
	}
	environment, err := owned.environmentBlock(spec.Environment())
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return application, commandLine, directory, environment, nil
}

func (*windowsProcess) environmentBlock(environment []string) ([]uint16, error) {
	values := append([]string(nil), environment...)
	sort.Slice(values, func(left, right int) bool {
		return strings.ToUpper(values[left]) < strings.ToUpper(values[right])
	})
	block := make([]uint16, 0, 2)
	for _, value := range values {
		if strings.IndexByte(value, 0) >= 0 {
			return nil, errors.New("windows process environment is invalid")
		}
		block = append(block, utf16.Encode([]rune(value))...)
		block = append(block, 0)
	}
	block = append(block, 0)
	if len(block) == 1 {
		block = append(block, 0)
	}
	return block, nil
}

func (*windowsProcess) newPlatformJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), // #nosec G103 -- exact Windows job structure.
		uint32(unsafe.Sizeof(limits)),    // #nosec G115 -- static Windows structure size.
	)
	if err != nil {
		return 0, errors.Join(err, (windowsHandleSet{}).closeHandle(job))
	}
	return job, nil
}

func (*windowsProcess) String() string         { return "processplatform.Process([REDACTED])" }
func (owned *windowsProcess) GoString() string { return owned.String() }
func (*windowsProcess) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "processplatform.Process([REDACTED])") //nolint:errcheck // fmt.Formatter cannot return an error.
}
func (owned *windowsProcess) LogValue() slog.Value { return slog.StringValue(owned.String()) }
