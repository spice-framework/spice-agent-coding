//go:build linux || darwin

package localipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const maximumUnixSocketPathBytes = 100

// Listen creates a Unix-domain stream listener at the exact absolute address.
// Its existing, symlink-free parent directory must be owned by the effective
// user and grant no group or other access; every ancestor must have a trusted
// owner and safe write semantics. An owned private stale socket is removed only
// after a failed active-listener probe; every other existing node is preserved.
func Listen(address string) (net.Listener, error) {
	if err := validateUnixAddress(address); err != nil {
		return nil, err
	}
	directory, err := openPrivateUnixDirectory(filepath.Dir(address))
	if err != nil {
		return nil, err
	}
	keepDirectory := false
	defer func() {
		if !keepDirectory {
			_ = directory.close()
		}
	}()
	name := filepath.Base(address)
	if err = prepareUnixSocket(directory, name, address); err != nil {
		return nil, err
	}
	raw, err := net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on local IPC endpoint: %w", err)
	}
	// Keep Go's unlink-on-close safeguard enabled until the socket and its
	// still-bound directory have both been verified.
	if err = directory.verifyPathIdentity(); err != nil {
		_ = raw.Close()
		return nil, err
	}
	if err = os.Chmod(address, 0o600); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("secure local IPC endpoint: %w", err)
	}
	identity, err := directory.validatePrivateSocket(name)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	raw.SetUnlinkOnClose(false)
	keepDirectory = true
	return &unixListener{UnixListener: raw, directory: directory, name: name, identity: identity}, nil
}

// Dial connects only to an explicitly addressed private Unix socket and
// honors caller cancellation and deadlines.
func Dial(ctx context.Context, address string) (net.Conn, error) {
	if ctx == nil {
		return nil, errors.New("local IPC dial context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateUnixAddress(address); err != nil {
		return nil, err
	}
	directory, err := openPrivateUnixDirectory(filepath.Dir(address))
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.close() }()
	if _, err = directory.validatePrivateSocket(filepath.Base(address)); err != nil {
		return nil, err
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", address)
	if err != nil {
		return nil, fmt.Errorf("dial local IPC endpoint: %w", err)
	}
	return connection, nil
}

type unixListener struct {
	*net.UnixListener
	directory *unixDirectory
	name      string
	identity  unixFileIdentity

	closeOnce sync.Once
	closeErr  error
}

func (listener *unixListener) Close() error {
	if listener == nil {
		return nil
	}
	listener.closeOnce.Do(func() {
		listener.closeErr = errors.Join(
			listener.UnixListener.Close(),
			listener.directory.removeOwnedSocket(listener.name, listener.identity),
			listener.directory.close(),
		)
	})
	return listener.closeErr
}

func validateUnixAddress(address string) error {
	if address == "" || strings.TrimSpace(address) != address || strings.IndexByte(address, 0) >= 0 ||
		!filepath.IsAbs(address) || filepath.Clean(address) != address || len(address) > maximumUnixSocketPathBytes {
		return fmt.Errorf("%w: Unix socket address must be clean, absolute, and at most %d bytes", ErrUnsafeEndpoint, maximumUnixSocketPathBytes)
	}
	name := filepath.Base(address)
	if name == "." || name == string(filepath.Separator) || !safeEndpointName(name) {
		return fmt.Errorf("%w: Unix socket name is invalid", ErrUnsafeEndpoint)
	}
	return nil
}

func safeEndpointName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, current := range name {
		if current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' ||
			current >= '0' && current <= '9' || strings.ContainsRune("._-", current) {
			continue
		}
		return false
	}
	return true
}

type unixDirectory struct {
	path     string
	file     int
	identity unixFileIdentity
}

type unixFileIdentity struct {
	device uint64
	inode  uint64
}

func openPrivateUnixDirectory(path string) (*unixDirectory, error) {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) || cleaned != path {
		return nil, fmt.Errorf("%w: Unix socket directory must be canonical and absolute", ErrUnsafeEndpoint)
	}
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, fmt.Errorf("open local IPC directory root: %w", err)
	}
	trimmed := strings.TrimPrefix(cleaned, string(filepath.Separator))
	if trimmed != "" {
		for component := range strings.SplitSeq(trimmed, string(filepath.Separator)) {
			next, openErr := unix.Openat(
				current, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0,
			)
			if openErr != nil {
				_ = unix.Close(current)
				return nil, fmt.Errorf("%w: open Unix socket directory component: %v", ErrUnsafeEndpoint, openErr)
			}
			if trustErr := validateUnixAncestryPair(current, next); trustErr != nil {
				_ = unix.Close(next)
				_ = unix.Close(current)
				return nil, trustErr
			}
			_ = unix.Close(current)
			current = next
		}
	}
	var stat unix.Stat_t
	if err = unix.Fstat(current, &stat); err != nil {
		_ = unix.Close(current)
		return nil, fmt.Errorf("inspect local IPC directory: %w", err)
	}
	mode := uint32(stat.Mode)
	if mode&unix.S_IFMT != unix.S_IFDIR || int(stat.Uid) != os.Geteuid() || mode&0o777 != 0o700 {
		_ = unix.Close(current)
		return nil, fmt.Errorf("%w: Unix socket directory must be owned and mode 0700", ErrUnsafeEndpoint)
	}
	return &unixDirectory{path: path, file: current, identity: unixIdentity(stat)}, nil
}

func validateUnixAncestryPair(parent, child int) error {
	var parentStat, childStat unix.Stat_t
	if err := unix.Fstat(parent, &parentStat); err != nil {
		return fmt.Errorf("inspect local IPC ancestor: %w", err)
	}
	if err := unix.Fstat(child, &childStat); err != nil {
		return fmt.Errorf("inspect local IPC directory: %w", err)
	}
	if !trustedUnixOwner(parentStat.Uid) {
		return fmt.Errorf("%w: Unix socket ancestor has an untrusted owner", ErrUnsafeEndpoint)
	}
	parentMode := uint32(parentStat.Mode)
	if parentMode&0o022 != 0 && (parentMode&unix.S_ISVTX == 0 || !trustedUnixOwner(childStat.Uid)) {
		return fmt.Errorf("%w: Unix socket ancestry is writable by an untrusted user", ErrUnsafeEndpoint)
	}
	return nil
}

func trustedUnixOwner(owner uint32) bool {
	return owner == 0 || int(owner) == os.Geteuid()
}

func (directory *unixDirectory) verifyPathIdentity() error {
	current, err := openPrivateUnixDirectory(directory.path)
	if err != nil {
		return err
	}
	defer func() { _ = current.close() }()
	if current.identity != directory.identity {
		return fmt.Errorf("%w: Unix socket directory identity changed", ErrUnsafeEndpoint)
	}
	return nil
}

func (directory *unixDirectory) validatePrivateSocket(name string) (unixFileIdentity, error) {
	if directory == nil || directory.file < 0 || !safeEndpointName(name) {
		return unixFileIdentity{}, ErrUnsafeEndpoint
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(directory.file, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unixFileIdentity{}, fmt.Errorf("inspect local IPC socket: %w", err)
	}
	mode := uint32(stat.Mode)
	if mode&unix.S_IFMT != unix.S_IFSOCK || mode&0o077 != 0 || int(stat.Uid) != os.Geteuid() {
		return unixFileIdentity{}, fmt.Errorf("%w: endpoint must be an owned private Unix socket", ErrUnsafeEndpoint)
	}
	return unixIdentity(stat), nil
}

func (directory *unixDirectory) close() error {
	if directory == nil || directory.file < 0 {
		return nil
	}
	err := unix.Close(directory.file)
	directory.file = -1
	return err
}

func unixIdentity(stat unix.Stat_t) unixFileIdentity {
	return unixFileIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}
}

func prepareUnixSocket(directory *unixDirectory, name, path string) error {
	identity, err := directory.validatePrivateSocket(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return ErrEndpointInUse
	}
	if !errors.Is(dialErr, unix.ECONNREFUSED) {
		return fmt.Errorf("%w: cannot prove existing Unix socket is stale: %w", ErrUnsafeEndpoint, dialErr)
	}
	if err = directory.removeOwnedSocket(name, identity); err != nil {
		return fmt.Errorf("remove stale local IPC socket: %w", err)
	}
	return nil
}

func (directory *unixDirectory) removeOwnedSocket(name string, expected unixFileIdentity) error {
	current, err := directory.validatePrivateSocket(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if expected != current {
		return fmt.Errorf("%w: Unix socket identity changed", ErrUnsafeEndpoint)
	}
	return unix.Unlinkat(directory.file, name, 0)
}
