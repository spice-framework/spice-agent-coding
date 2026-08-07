// Package userstorage provides retained, current-user-only local storage for
// daemon security state. Paths are bound once and all subsequent operations
// are relative to the retained operating-system directory identity.
package userstorage

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
)

var (
	// ErrUnavailable reports that storage could not be proven to satisfy the
	// current-user ownership, ancestry, file-identity, or platform contract.
	ErrUnavailable = errors.New("secure user storage is unavailable")
	// ErrLockBusy reports that another process owns the requested stable lock.
	ErrLockBusy = errors.New("secure user storage lock is busy")
	errLockBusy = ErrLockBusy
)

// Directory is one exact absolute current-user directory retained by an OS
// descriptor or handle. It is safe to close repeatedly.
type Directory struct {
	value *secureDirectory
}

// Lock owns one process-stable exclusive file lock. It is safe to close
// repeatedly and concurrently.
type Lock struct {
	mu     sync.Mutex
	value  *stableLock
	closed bool
}

// Bind creates or opens path and proves its complete ancestry and leaf are
// safe for current-user security state. Path must be clean and absolute.
func Bind(path string) (*Directory, error) {
	if path == "" || path != filepath.Clean(path) || !filepath.IsAbs(path) {
		return nil, ErrUnavailable
	}
	value, err := bindSecureDirectory(path)
	if err != nil {
		return nil, err
	}
	return &Directory{value: value}, nil
}

// ReadFile reads at most maximum bytes from one regular current-user file
// relative to the retained directory identity.
func (directory *Directory) ReadFile(name string, maximum int) ([]byte, error) {
	if directory == nil || directory.value == nil || maximum < 0 {
		return nil, ErrUnavailable
	}
	return directory.value.readFile(name, maximum)
}

// WriteFileAtomic durably replaces one regular current-user file relative to
// the retained directory identity. Temporary files and the final file use the
// platform's current-user-only protection.
func (directory *Directory) WriteFileAtomic(name string, value []byte) error {
	if directory == nil || directory.value == nil {
		return ErrUnavailable
	}
	return directory.value.writeFileAtomic(name, value)
}

// RemoveFile removes one validated regular current-user file relative to the
// retained directory identity. A missing file is already removed and succeeds.
func (directory *Directory) RemoveFile(name string) error {
	if directory == nil || directory.value == nil {
		return ErrUnavailable
	}
	return directory.value.removeFile(name)
}

// AcquireLock acquires a non-blocking, process-stable exclusive lock relative
// to the retained directory. Contention returns ErrLockBusy.
func (directory *Directory) AcquireLock(name string) (*Lock, error) {
	if directory == nil || directory.value == nil {
		return nil, ErrUnavailable
	}
	value, err := directory.value.acquireStableLock(name)
	if err != nil {
		return nil, err
	}
	return &Lock{value: value}, nil
}

// AcquireInitializationLock waits for the process-stable exclusive lock used
// to serialize bounded initialization work within a retained directory.
func (directory *Directory) AcquireInitializationLock(name string) (*Lock, error) {
	if directory == nil || directory.value == nil {
		return nil, ErrUnavailable
	}
	value, err := directory.value.acquireInitializationLock(name)
	if err != nil {
		return nil, err
	}
	return &Lock{value: value}, nil
}

// Close releases the retained directory identity. It is idempotent.
func (directory *Directory) Close() error {
	if directory == nil || directory.value == nil {
		return nil
	}
	return directory.value.close()
}

// Close releases the stable lock. It is idempotent and concurrency-safe.
func (lock *Lock) Close() error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.closed || lock.value == nil {
		return nil
	}
	lock.closed = true
	return lock.value.close()
}

// ReadFile securely reads an absolute file path whose existing parent is
// rebound for this operation.
func ReadFile(path string, maximum int) ([]byte, error) {
	if path == "" || path != filepath.Clean(path) || !filepath.IsAbs(path) || maximum < 0 {
		return nil, ErrUnavailable
	}
	return readSecureFile(path, maximum)
}

// WriteFileAtomic securely replaces an absolute file path whose existing
// parent is rebound for this operation.
func WriteFileAtomic(path string, value []byte) error {
	if path == "" || path != filepath.Clean(path) || !filepath.IsAbs(path) {
		return ErrUnavailable
	}
	return writeSecureFileAtomic(path, value)
}

// AcquireStableLock securely acquires a non-blocking lock at an absolute path.
func AcquireStableLock(path string) (*Lock, error) {
	if path == "" || path != filepath.Clean(path) || !filepath.IsAbs(path) {
		return nil, ErrUnavailable
	}
	value, err := acquireStableLock(path)
	if err != nil {
		return nil, err
	}
	return &Lock{value: value}, nil
}

func validRelativeName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name &&
		filepath.VolumeName(name) == "" && !strings.ContainsRune(name, 0)
}
