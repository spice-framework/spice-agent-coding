//go:build windows

package devacceptance

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func configureDevelopmentCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
	}
}

func interruptDevelopmentCommand(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return os.ErrProcessDone
	}
	return windows.GenerateConsoleCtrlEvent(
		windows.CTRL_BREAK_EVENT,
		uint32(command.Process.Pid), // #nosec G115 -- Windows process identifiers are unsigned 32-bit values.
	)
}

func killDevelopmentSupervisor(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return os.ErrProcessDone
	}
	return exec.Command( // #nosec G204,G702 -- fixed system utility and a decimal process identifier.
		"taskkill.exe",
		"/PID",
		strconv.Itoa(command.Process.Pid),
		"/T",
		"/F",
	).Run()
}

func killDevelopmentChild(_ int) error { return nil }

func replaceFile(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = windows.MoveFileEx(
			from,
			to,
			windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
		)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return os.ErrNotExist
		}
		if err == nil || !transientReplaceContention(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func transientReplaceContention(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
