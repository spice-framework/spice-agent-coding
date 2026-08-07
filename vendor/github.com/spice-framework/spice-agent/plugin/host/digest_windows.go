//go:build windows

package pluginhost

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type fileIdentity struct {
	volumeSerial uint32
	fileIndexHi  uint32
	fileIndexLo  uint32
}

func (identity fileIdentity) equal(other fileIdentity) bool { return identity == other }

func openExecutableFile(path string) (*os.File, fileIdentity, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fileIdentity{}, err
	}
	// Denying FILE_SHARE_WRITE and FILE_SHARE_DELETE prevents ordinary in-place
	// mutation or pathname replacement while the launch lease is held.
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fileIdentity{}, err
	}
	file := os.NewFile(uintptr(handle), "[plugin-executable]")
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fileIdentity{}, errors.New("cannot own plugin executable handle")
	}
	identity, err := inspectExecutableHandle(handle)
	if err != nil {
		_ = file.Close()
		return nil, fileIdentity{}, err
	}
	return file, identity, nil
}

func inspectExecutableHandle(handle windows.Handle) (fileIdentity, error) {
	fileType, err := windows.GetFileType(handle)
	if err != nil {
		return fileIdentity{}, err
	}
	if fileType != windows.FILE_TYPE_DISK {
		return fileIdentity{}, errors.New("plugin executable is not a disk file")
	}
	var information windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &information); err != nil {
		return fileIdentity{}, err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fileIdentity{}, errors.New("plugin executable is a reparse point")
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return fileIdentity{}, errors.New("plugin executable is not a regular file")
	}
	return fileIdentity{
		volumeSerial: information.VolumeSerialNumber,
		fileIndexHi:  information.FileIndexHigh,
		fileIndexLo:  information.FileIndexLow,
	}, nil
}
