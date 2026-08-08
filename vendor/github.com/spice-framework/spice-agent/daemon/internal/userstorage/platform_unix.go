//go:build linux || darwin

package userstorage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

type stableLock struct {
	file *os.File
}

type secureDirectory struct {
	mu   sync.RWMutex
	path string
	fd   int
}

func bindSecureDirectory(path string) (*secureDirectory, error) {
	descriptor, created, err := createDirectoryNoFollow(path)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(descriptor, &stat); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	mode := uint32(stat.Mode) //nolint:unconvert // Required on Darwin; redundant only on Linux.
	if mode&unix.S_IFMT != unix.S_IFDIR || int(stat.Uid) != os.Geteuid() {
		_ = unix.Close(descriptor)
		return nil, ErrUnavailable
	}
	if !created && mode&0o777 != 0o700 {
		_ = unix.Close(descriptor)
		return nil, ErrUnavailable
	}
	if created {
		err = unix.Fchmod(descriptor, 0o700)
	}
	if err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	if err = validateUnixAncestry(path, descriptor); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	return &secureDirectory{path: path, fd: descriptor}, nil
}

func acquireStableLock(path string) (*stableLock, error) {
	directory, err := bindExistingDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.close() }()
	return directory.acquireStableLock(filepath.Base(path))
}

func (directory *secureDirectory) acquireStableLock(name string) (*stableLock, error) {
	return directory.acquireLock(name, false)
}

func (directory *secureDirectory) acquireInitializationLock(name string) (*stableLock, error) {
	return directory.acquireLock(name, true)
}

func (directory *secureDirectory) acquireLock(name string, wait bool) (*stableLock, error) {
	if !validRelativeName(name) {
		return nil, ErrUnavailable
	}
	directory.mu.RLock()
	defer directory.mu.RUnlock()
	if directory.fd < 0 {
		return nil, ErrUnavailable
	}
	fileDescriptor, err := unix.Openat(directory.fd, name, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fileDescriptor), filepath.Join(directory.path, name))
	if err = validateUnixFile(fileDescriptor, 0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	flags := unix.LOCK_EX | unix.LOCK_NB
	if wait {
		flags = unix.LOCK_EX
	}
	if err = unix.Flock(fileDescriptor, flags); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errLockBusy
		}
		return nil, err
	}
	return &stableLock{file: file}, nil
}

func (lock *stableLock) close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	descriptor := int(lock.file.Fd())
	unlockErr := unix.Flock(descriptor, unix.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	return errors.Join(unlockErr, closeErr)
}

func readSecureFile(path string, maximum int) ([]byte, error) {
	directory, err := bindExistingDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.close() }()
	return directory.readFile(filepath.Base(path), maximum)
}

func (directory *secureDirectory) readFile(name string, maximum int) ([]byte, error) {
	if !validRelativeName(name) {
		return nil, ErrUnavailable
	}
	directory.mu.RLock()
	defer directory.mu.RUnlock()
	if directory.fd < 0 {
		return nil, ErrUnavailable
	}
	fileDescriptor, err := unix.Openat(directory.fd, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fileDescriptor), filepath.Join(directory.path, name))
	defer func() { _ = file.Close() }()
	if err = validateUnixFile(fileDescriptor, 0o600); err != nil {
		return nil, err
	}
	value, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(value) > maximum {
		return nil, ErrUnavailable
	}
	return value, nil
}

func writeSecureFileAtomic(path string, value []byte) (resultErr error) {
	directory, err := bindExistingDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.close() }()
	return directory.writeFileAtomic(filepath.Base(path), value)
}

func (directory *secureDirectory) writeFileAtomic(name string, value []byte) (resultErr error) {
	if !validRelativeName(name) {
		return ErrUnavailable
	}
	directory.mu.RLock()
	defer directory.mu.RUnlock()
	if directory.fd < 0 {
		return ErrUnavailable
	}
	if err := validateUnixDestination(directory.fd, name); err != nil {
		return err
	}
	var random [12]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return err
	}
	temporary := "." + name + "." + hex.EncodeToString(random[:]) + ".tmp"
	fileDescriptor, err := unix.Openat(directory.fd, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fileDescriptor), temporary)
	defer func() {
		_ = file.Close()
		_ = unix.Unlinkat(directory.fd, temporary, 0)
	}()
	if _, err = file.Write(value); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = unix.Renameat(directory.fd, temporary, directory.fd, name); err != nil {
		return err
	}
	if err = unix.Fsync(directory.fd); err != nil {
		return err
	}
	return nil
}

func (directory *secureDirectory) removeFile(name string) error {
	if !validRelativeName(name) {
		return ErrUnavailable
	}
	directory.mu.RLock()
	defer directory.mu.RUnlock()
	if directory.fd < 0 {
		return ErrUnavailable
	}
	fileDescriptor, err := unix.Openat(directory.fd, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fileDescriptor) }()
	if err = validateUnixFile(fileDescriptor, 0o600); err != nil {
		return err
	}
	var opened, current unix.Stat_t
	if err = unix.Fstat(fileDescriptor, &opened); err != nil {
		return err
	}
	if err = unix.Fstatat(directory.fd, name, &current, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	currentMode := uint32(current.Mode) //nolint:unconvert // Required on Darwin; redundant only on Linux.
	if opened.Dev != current.Dev || opened.Ino != current.Ino ||
		currentMode&unix.S_IFMT != unix.S_IFREG || int(current.Uid) != os.Geteuid() ||
		current.Nlink != 1 || currentMode&0o777 != 0o600 {
		return ErrUnavailable
	}
	if err = unix.Unlinkat(directory.fd, name, 0); errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

func bindExistingDirectory(path string) (*secureDirectory, error) {
	if path == "" || path != filepath.Clean(path) || !filepath.IsAbs(path) {
		return nil, ErrUnavailable
	}
	descriptor, err := openDirectoryNoFollow(path)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(descriptor, &stat); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	mode := uint32(stat.Mode) //nolint:unconvert // Required on Darwin; redundant only on Linux.
	if mode&unix.S_IFMT != unix.S_IFDIR || int(stat.Uid) != os.Geteuid() || mode&0o777 != 0o700 {
		_ = unix.Close(descriptor)
		return nil, ErrUnavailable
	}
	if err = validateUnixAncestry(path, descriptor); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	return &secureDirectory{path: path, fd: descriptor}, nil
}

func (directory *secureDirectory) close() error {
	if directory == nil {
		return nil
	}
	directory.mu.Lock()
	defer directory.mu.Unlock()
	if directory.fd < 0 {
		return nil
	}
	err := unix.Close(directory.fd)
	directory.fd = -1
	return err
}

func openDirectoryNoFollow(path string) (int, error) {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return -1, ErrUnavailable
	}
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY, 0)
	if err != nil {
		return -1, err
	}
	trimmed := strings.TrimPrefix(cleaned, string(filepath.Separator))
	if trimmed == "" {
		return current, nil
	}
	for component := range strings.SplitSeq(trimmed, string(filepath.Separator)) {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return -1, unixPathError(openErr)
		}
		current = next
	}
	return current, nil
}

func createDirectoryNoFollow(path string) (int, bool, error) {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return -1, false, ErrUnavailable
	}
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY, 0)
	if err != nil {
		return -1, false, err
	}
	trimmed := strings.TrimPrefix(cleaned, string(filepath.Separator))
	if trimmed == "" {
		_ = unix.Close(current)
		return -1, false, ErrUnavailable
	}
	componentCount := strings.Count(trimmed, string(filepath.Separator)) + 1
	componentIndex := 0
	leafCreated := false
	for component := range strings.SplitSeq(trimmed, string(filepath.Separator)) {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) {
			mkdirErr := unix.Mkdirat(current, component, 0o700)
			if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(current)
				return -1, false, mkdirErr
			}
			if mkdirErr == nil && componentIndex == componentCount-1 {
				leafCreated = true
			}
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		}
		_ = unix.Close(current)
		if openErr != nil {
			return -1, false, unixPathError(openErr)
		}
		current = next
		componentIndex++
	}
	return current, leafCreated, nil
}

func unixPathError(err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return ErrUnavailable
	}
	return err
}

func validateUnixAncestry(path string, expected int) error {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return ErrUnavailable
	}
	parent, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	trimmed := strings.TrimPrefix(cleaned, string(filepath.Separator))
	for component := range strings.SplitSeq(trimmed, string(filepath.Separator)) {
		child, openErr := unix.Openat(parent, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = unix.Close(parent)
			return openErr
		}
		if trustErr := validateUnixAncestryPair(parent, child); trustErr != nil {
			_ = unix.Close(child)
			_ = unix.Close(parent)
			return trustErr
		}
		_ = unix.Close(parent)
		parent = child
	}
	var actualStat, expectedStat unix.Stat_t
	statErr := unix.Fstat(parent, &actualStat)
	if statErr == nil {
		statErr = unix.Fstat(expected, &expectedStat)
	}
	if statErr == nil && (actualStat.Dev != expectedStat.Dev || actualStat.Ino != expectedStat.Ino) {
		statErr = ErrUnavailable
	}
	return errors.Join(statErr, unix.Close(parent))
}

func validateUnixAncestryPair(parent, child int) error {
	var parentStat, childStat unix.Stat_t
	if err := unix.Fstat(parent, &parentStat); err != nil {
		return err
	}
	if err := unix.Fstat(child, &childStat); err != nil {
		return err
	}
	if !trustedUnixOwner(parentStat.Uid) {
		return ErrUnavailable
	}
	parentMode := uint32(parentStat.Mode) //nolint:unconvert // Required on Darwin; redundant only on Linux.
	if parentMode&0o022 == 0 {
		return nil
	}
	if parentMode&unix.S_ISVTX == 0 || !trustedUnixOwner(childStat.Uid) {
		return ErrUnavailable
	}
	return nil
}

func trustedUnixOwner(owner uint32) bool {
	return owner == 0 || int(owner) == os.Geteuid()
}

func validateUnixDestination(directory int, base string) error {
	var stat unix.Stat_t
	err := unix.Fstatat(directory, base, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	mode := uint32(stat.Mode) //nolint:unconvert // Required on Darwin; redundant only on Linux.
	if mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 || mode&0o777 != 0o600 {
		return ErrUnavailable
	}
	return nil
}

func validateUnixFile(descriptor int, mode uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(descriptor, &stat); err != nil {
		return err
	}
	statMode := uint32(stat.Mode) //nolint:unconvert // Required on Darwin; redundant only on Linux.
	if statMode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 || statMode&0o777 != mode {
		return ErrUnavailable
	}
	return nil
}
