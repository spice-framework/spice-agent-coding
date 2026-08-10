//go:build windows

package daemonprocess

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processFactory struct{}

func (processFactory) start(spec processSpec) (launchedProcess, error) {
	factory := processFactory{}
	if err := factory.validate(spec); err != nil {
		return nil, err
	}

	job, err := (windowsJob{}).open()
	if err != nil {
		return nil, err
	}
	pipes, err := (&windowsProcessPipes{}).open()
	if err != nil {
		return nil, errors.Join(err, (windowsHandleOwner{}).close(job))
	}
	published := false
	defer func() {
		if !published {
			_ = pipes.closeAll()                  //nolint:errcheck // The primary setup error remains authoritative.
			_ = (windowsHandleOwner{}).close(job) //nolint:errcheck // The primary setup error remains authoritative.
		}
	}()

	application, commandLine, directory, environment, err := factory.parameters(spec)
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
	return factory.finish(job, pipes, information, spec)
}

func (processFactory) validate(spec processSpec) error {
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

func (processFactory) parameters(spec processSpec) (*uint16, *uint16, *uint16, []uint16, error) {
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
	environment, err := (windowsEnvironment{}).block(spec.environment)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return application, commandLine, directory, environment, nil
}

func (processFactory) finish(
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
		child, abortErr := (processFactory{}).abort(
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
		child, abortErr := (processFactory{}).abort(job, information, true, pipes, spec, resumeErr, nil)
		if child != nil {
			pipes.releaseAll()
		}
		return child, abortErr
	}
	if err = (windowsHandleOwner{}).close(information.Thread); err != nil {
		child, abortErr := (processFactory{}).abort(job, information, true, pipes, spec, err, err)
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

func (processFactory) abort(
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
	waitErr := (windowsHandleOwner{}).wait(information.Process, windowsCleanupTimeout)
	var jobDrainErr error
	if assigned {
		jobDrainErr = (windowsJob{}).waitEmpty(job, windowsCleanupTimeout)
	}
	cleanupErr := errors.Join(initialCleanupError, terminationErr, waitErr, jobDrainErr)
	if cleanupErr != nil {
		return (processFactory{}).failed(job, information, assigned, pipes, spec, cleanupErr, false, nil),
			errors.Join(cause, cleanupErr)
	}
	outcome := (processFactory{}).outcome(information.Process)
	threadCloseErr := (windowsHandleOwner{}).close(information.Thread)
	if threadCloseErr == nil {
		information.Thread = 0
	}
	jobCloseErr := (windowsHandleOwner{}).close(job)
	if jobCloseErr == nil {
		job = 0
	}
	processCloseErr := (windowsHandleOwner{}).close(information.Process)
	if processCloseErr == nil {
		information.Process = 0
	}
	pipeCloseErr := pipes.closeAll()
	cleanupErr = errors.Join(threadCloseErr, jobCloseErr, processCloseErr, pipeCloseErr)
	if cleanupErr != nil {
		return (processFactory{}).failed(job, information, assigned, pipes, spec, cleanupErr, true, outcome),
			errors.Join(cause, cleanupErr)
	}
	return nil, cause
}

func (processFactory) failed(
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

func (processFactory) outcome(handle windows.Handle) error {
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return err
	}
	if exitCode == 0 {
		return nil
	}
	return &windowsExitError{code: exitCode}
}
