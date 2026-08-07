package endpoint

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spice-framework/spice-agent/daemon/internal/userstorage"
)

// UserScope is the immutable current-user storage and local-transport identity for
// one Spice Agent installation. Its fields are deliberately private so callers
// cannot separate a trusted storage directory from its platform address.
type UserScope struct {
	directory string
	transport Transport
	address   string
}

// CurrentUserScope selects and validates the current user's private runtime
// directory and stable local endpoint. It never accepts a remote transport or
// a directory whose ownership and ancestry cannot be proven by the operating
// system.
func CurrentUserScope() (UserScope, error) {
	return currentUserScope()
}

// Directory returns the canonical private directory used for endpoint state.
func (scope UserScope) Directory() string { return scope.directory }

// Transport returns the current platform's local-only transport.
func (scope UserScope) Transport() Transport { return scope.transport }

// Address returns the stable canonical local endpoint address.
func (scope UserScope) Address() string { return scope.address }

// Validate rebinds the runtime directory through the secure storage boundary
// and validates that the transport and address still form a supported scope.
func (scope UserScope) Validate() error {
	if err := scope.validateFields(); err != nil {
		return err
	}
	directory, err := userstorage.Bind(scope.directory)
	if err != nil {
		return fmt.Errorf("validate current-user endpoint directory: %w", err)
	}
	if err = directory.Close(); err != nil {
		return fmt.Errorf("close current-user endpoint directory: %w", err)
	}
	return nil
}

// OpenStore opens endpoint coordination state inside this exact scope.
// PollInterval has the same positive-duration contract as OpenStore.
func (scope UserScope) OpenStore(pollInterval time.Duration) (*Store, error) {
	if err := scope.validateFields(); err != nil {
		return nil, err
	}
	return OpenStore(StoreConfig{Directory: scope.directory, PollInterval: pollInterval})
}

func newUserScope(directory string, transport Transport, address string) (UserScope, error) {
	scope := UserScope{directory: directory, transport: transport, address: address}
	if err := scope.Validate(); err != nil {
		return UserScope{}, err
	}
	return scope, nil
}

func (scope UserScope) validateFields() error {
	if scope.directory == "" || !filepath.IsAbs(scope.directory) ||
		filepath.Clean(scope.directory) != scope.directory {
		return errors.New("current-user endpoint directory must be a canonical absolute path")
	}
	if err := validateAddress(scope.transport, scope.address); err != nil {
		return err
	}
	if err := validateScopePlatform(scope); err != nil {
		return err
	}
	return nil
}
