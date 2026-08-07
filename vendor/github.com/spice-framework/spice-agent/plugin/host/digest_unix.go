//go:build unix

package pluginhost

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type fileIdentity struct {
	device uint64
	inode  uint64
}

func (identity fileIdentity) equal(other fileIdentity) bool { return identity == other }

func openExecutableFile(path string) (*os.File, fileIdentity, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fileIdentity{}, err
	}
	file := os.NewFile(uintptr(descriptor), "[plugin-executable]")
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, fileIdentity{}, errors.New("cannot own plugin executable descriptor")
	}
	identity, err := inspectExecutableDescriptor(descriptor)
	if err != nil {
		_ = file.Close()
		return nil, fileIdentity{}, err
	}
	return file, identity, nil
}

func inspectExecutableDescriptor(descriptor int) (fileIdentity, error) {
	var status unix.Stat_t
	if err := unix.Fstat(descriptor, &status); err != nil {
		return fileIdentity{}, err
	}
	if status.Mode&unix.S_IFMT != unix.S_IFREG {
		return fileIdentity{}, errors.New("plugin executable is not a regular file")
	}
	if status.Mode&0o111 == 0 {
		return fileIdentity{}, errors.New("plugin executable has no execute permission")
	}
	return fileIdentity{device: uint64(status.Dev), inode: uint64(status.Ino)}, nil
}
