//go:build windows

package processplatform

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type platformPipes struct {
	childInput, childOutput, childStderr    windows.Handle
	parentInput, parentOutput, parentStderr *os.File
}

func (pipes *platformPipes) initialize() error {
	security := windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})), // #nosec G115 -- static Windows structure size.
		InheritHandle: 1,
	}
	var childInput, parentInput windows.Handle
	if err := windows.CreatePipe(&childInput, &parentInput, &security, 0); err != nil {
		return err
	}
	var parentOutput, childOutput windows.Handle
	handles := windowsHandleSet{}
	if err := windows.CreatePipe(&parentOutput, &childOutput, &security, 0); err != nil {
		return errors.Join(err, handles.closeHandle(childInput), handles.closeHandle(parentInput))
	}
	var parentStderr, childStderr windows.Handle
	if err := windows.CreatePipe(&parentStderr, &childStderr, &security, 0); err != nil {
		return errors.Join(err, handles.close(childInput, parentInput, parentOutput, childOutput))
	}
	if err := errors.Join(
		windows.SetHandleInformation(parentInput, windows.HANDLE_FLAG_INHERIT, 0),
		windows.SetHandleInformation(parentOutput, windows.HANDLE_FLAG_INHERIT, 0),
		windows.SetHandleInformation(parentStderr, windows.HANDLE_FLAG_INHERIT, 0),
	); err != nil {
		return errors.Join(err, handles.close(
			childInput, parentInput, parentOutput, childOutput, parentStderr, childStderr,
		))
	}
	pipes.childInput, pipes.childOutput, pipes.childStderr = childInput, childOutput, childStderr
	pipes.parentInput = os.NewFile(uintptr(parentInput), "process-stdin")
	pipes.parentOutput = os.NewFile(uintptr(parentOutput), "process-stdout")
	pipes.parentStderr = os.NewFile(uintptr(parentStderr), "process-stderr")
	return nil
}

func (pipes *platformPipes) closeChildEnds() error {
	if pipes == nil {
		return nil
	}
	err := (windowsHandleSet{}).close(pipes.childInput, pipes.childOutput, pipes.childStderr)
	pipes.childInput, pipes.childOutput, pipes.childStderr = 0, 0, 0
	return err
}

func (pipes *platformPipes) closeAll() error {
	if pipes == nil {
		return nil
	}
	handles := windowsHandleSet{}
	return errors.Join(
		pipes.closeChildEnds(), handles.closeFile(pipes.parentInput),
		handles.closeFile(pipes.parentOutput), handles.closeFile(pipes.parentStderr),
	)
}

func (pipes *platformPipes) releaseParentEnds() {
	pipes.parentInput, pipes.parentOutput, pipes.parentStderr = nil, nil, nil
}
