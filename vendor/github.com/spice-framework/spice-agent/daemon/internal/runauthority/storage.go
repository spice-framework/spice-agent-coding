package runauthority

import (
	"errors"

	"github.com/spice-framework/spice-agent/daemon/internal/userstorage"
)

var errLockBusy = userstorage.ErrLockBusy

type secureDirectory struct {
	value *userstorage.Directory
}

type stableLock struct {
	value   *userstorage.Lock
	closeFn func() error
}

func bindSecureDirectory(path string) (*secureDirectory, error) {
	value, err := userstorage.Bind(path)
	if err != nil {
		return nil, translateStorageError(err)
	}
	return &secureDirectory{value: value}, nil
}

func acquireStableLock(path string) (*stableLock, error) {
	value, err := userstorage.AcquireStableLock(path)
	if err != nil {
		return nil, translateStorageError(err)
	}
	return wrapStableLock(value), nil
}

func (directory *secureDirectory) acquireStableLock(name string) (*stableLock, error) {
	if directory == nil || directory.value == nil {
		return nil, ErrUnavailable
	}
	value, err := directory.value.AcquireLock(name)
	if err != nil {
		return nil, translateStorageError(err)
	}
	return wrapStableLock(value), nil
}

func (directory *secureDirectory) acquireInitializationLock(name string) (*stableLock, error) {
	if directory == nil || directory.value == nil {
		return nil, ErrUnavailable
	}
	value, err := directory.value.AcquireInitializationLock(name)
	if err != nil {
		return nil, translateStorageError(err)
	}
	return wrapStableLock(value), nil
}

func wrapStableLock(value *userstorage.Lock) *stableLock {
	return &stableLock{value: value, closeFn: value.Close}
}

func (lock *stableLock) close() error {
	if lock == nil || lock.closeFn == nil {
		return nil
	}
	closeFn := lock.closeFn
	lock.closeFn = nil
	return closeFn()
}

func (directory *secureDirectory) readFile(name string, maximum int) ([]byte, error) {
	if directory == nil || directory.value == nil {
		return nil, ErrUnavailable
	}
	value, err := directory.value.ReadFile(name, maximum)
	return value, translateStorageError(err)
}

func (directory *secureDirectory) writeFileAtomic(name string, value []byte) error {
	if directory == nil || directory.value == nil {
		return ErrUnavailable
	}
	return translateStorageError(directory.value.WriteFileAtomic(name, value))
}

func (directory *secureDirectory) close() error {
	if directory == nil || directory.value == nil {
		return nil
	}
	return translateStorageError(directory.value.Close())
}

func readSecureFile(path string, maximum int) ([]byte, error) {
	value, err := userstorage.ReadFile(path, maximum)
	return value, translateStorageError(err)
}

func writeSecureFileAtomic(path string, value []byte) error {
	return translateStorageError(userstorage.WriteFileAtomic(path, value))
}

func translateStorageError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, userstorage.ErrLockBusy):
		return errLockBusy
	case errors.Is(err, userstorage.ErrUnavailable):
		return ErrUnavailable
	default:
		return err
	}
}
