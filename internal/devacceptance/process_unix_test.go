//go:build !windows

package devacceptance

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureDevelopmentCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func interruptDevelopmentCommand(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return os.ErrProcessDone
	}
	return syscall.Kill(-command.Process.Pid, syscall.SIGINT)
}

func killDevelopmentSupervisor(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return os.ErrProcessDone
	}
	return ignoreMissingProcess(syscall.Kill(-command.Process.Pid, syscall.SIGKILL))
}

func killDevelopmentChild(pid int) error {
	if pid <= 0 {
		return nil
	}
	group, err := syscall.Getpgid(pid)
	if err != nil {
		return ignoreMissingProcess(err)
	}
	if group != pid {
		return errors.New("refuse to terminate development child outside its expected process group")
	}
	return ignoreMissingProcess(syscall.Kill(-pid, syscall.SIGKILL))
}

func ignoreMissingProcess(err error) error {
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
