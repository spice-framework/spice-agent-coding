//go:build linux || darwin

package endpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/spice-framework/spice-agent/daemon/internal/userstorage"
	"golang.org/x/sys/unix"
)

const unixSocketName = "agent.sock"

func currentUserScope() (UserScope, error) {
	return currentUserUnixScope(os.Getenv("XDG_RUNTIME_DIR"), defaultStickyTemp(), os.Geteuid())
}

func currentUserUnixScope(xdgRuntimeDirectory, stickyTemp string, effectiveUserID int) (UserScope, error) {
	if effectiveUserID < 0 {
		return UserScope{}, errors.New("current user ID is invalid")
	}
	var xdgErr error
	if xdgRuntimeDirectory != "" {
		scope, err := xdgUserScope(xdgRuntimeDirectory, effectiveUserID)
		if err == nil {
			return scope, nil
		}
		xdgErr = fmt.Errorf("reject XDG_RUNTIME_DIR: %w", err)
	}
	if err := validateStickyTemp(stickyTemp, effectiveUserID); err != nil {
		return UserScope{}, errors.Join(
			xdgErr,
			fmt.Errorf("validate fallback runtime directory: %w", err),
		)
	}
	directory := filepath.Join(stickyTemp, "spice-agent-"+strconv.Itoa(effectiveUserID))
	scope, err := newUserScope(
		directory,
		TransportUnixSocket,
		filepath.Join(directory, unixSocketName),
	)
	if err != nil {
		return UserScope{}, errors.Join(xdgErr, fmt.Errorf("open fallback runtime scope: %w", err))
	}
	return scope, nil
}

func xdgUserScope(directory string, effectiveUserID int) (UserScope, error) {
	if err := validateXDGDirectory(directory, effectiveUserID); err != nil {
		return UserScope{}, err
	}
	scopeDirectory := filepath.Join(directory, "spice-agent")
	return newUserScope(
		scopeDirectory,
		TransportUnixSocket,
		filepath.Join(scopeDirectory, unixSocketName),
	)
}

func defaultStickyTemp() string {
	if runtime.GOOS == "darwin" {
		return "/private/tmp"
	}
	return "/tmp"
}

func validateXDGDirectory(directory string, effectiveUserID int) error {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("runtime directory must be a canonical absolute path")
	}
	descriptor, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(descriptor) }()
	var status unix.Stat_t
	if err = unix.Fstat(descriptor, &status); err != nil {
		return err
	}
	if uint32(status.Mode)&unix.S_IFMT != unix.S_IFDIR || int(status.Uid) != effectiveUserID ||
		uint32(status.Mode)&0o777 != 0o700 {
		return errors.New("runtime directory must be owned by the current user with mode 0700")
	}
	bound, err := userstorage.Bind(directory)
	if err != nil {
		return err
	}
	return bound.Close()
}

func validateStickyTemp(directory string, effectiveUserID int) error {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("temporary directory must be a canonical absolute path")
	}
	descriptor, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(descriptor) }()
	var status unix.Stat_t
	if err = unix.Fstat(descriptor, &status); err != nil {
		return err
	}
	mode := uint32(status.Mode)
	owner := int(status.Uid)
	if mode&unix.S_IFMT != unix.S_IFDIR || mode&unix.S_ISVTX == 0 ||
		(owner != 0 && owner != effectiveUserID) {
		return errors.New("temporary directory must be a trusted sticky directory")
	}
	if err = unix.Access(directory, unix.W_OK|unix.X_OK); err != nil {
		return errors.New("temporary directory is not writable by the current user")
	}
	return nil
}

func validateScopePlatform(scope UserScope) error {
	if scope.transport != TransportUnixSocket {
		return errors.New("current-user endpoint scope requires a Unix socket")
	}
	if filepath.Dir(scope.address) != scope.directory {
		return errors.New("unix endpoint address must be inside its current-user directory")
	}
	return nil
}
