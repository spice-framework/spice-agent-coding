//go:build windows

package daemonprocess

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessPipes struct {
	childInput   windows.Handle
	childOutput  windows.Handle
	childStderr  windows.Handle
	parentInput  *os.File
	parentStderr *os.File
}

func (*windowsProcessPipes) open() (*windowsProcessPipes, error) {
	handles := windowsHandleOwner{}
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
		return nil, errors.Join(err, handles.close(childInput), handles.close(parentInput))
	}
	nulName, err := windows.UTF16PtrFromString("NUL")
	if err != nil {
		return nil, errors.Join(err, handles.closeAll([]windows.Handle{childInput, parentInput, parentStderr, childStderr}))
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
		return nil, errors.Join(err, handles.closeAll([]windows.Handle{childInput, parentInput, parentStderr, childStderr}))
	}
	all := []windows.Handle{childInput, parentInput, parentStderr, childStderr, childOutput}
	if err = windows.SetHandleInformation(parentInput, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return nil, errors.Join(err, handles.closeAll(all))
	}
	if err = windows.SetHandleInformation(parentStderr, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return nil, errors.Join(err, handles.closeAll(all))
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
	err := (windowsHandleOwner{}).closeAll([]windows.Handle{pipes.childInput, pipes.childOutput, pipes.childStderr})
	pipes.childInput = 0
	pipes.childOutput = 0
	pipes.childStderr = 0
	return err
}

func (pipes *windowsProcessPipes) closeAll() error {
	if pipes == nil {
		return nil
	}
	return errors.Join(
		pipes.closeChildEnds(),
		(windowsHandleOwner{}).closeFile(pipes.parentInput),
		(windowsHandleOwner{}).closeFile(pipes.parentStderr),
	)
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
