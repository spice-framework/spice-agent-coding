//go:build spice_acceptance

package endpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
)

const acceptanceUnixSocketName = "agent.sock"

// AcceptanceUserScope derives an isolated endpoint scope below the protected
// current-user runtime directory. It exists only in spice_acceptance builds;
// production binaries cannot select endpoint storage or transport through
// configuration.
//
// The private newUserScope constructor still performs the ordinary secure
// directory bind and platform transport validation. Callers supply no address:
// it is deterministically derived from the validated isolated directory.
func AcceptanceUserScope(directory string) (UserScope, error) {
	current, err := CurrentUserScope()
	if err != nil {
		return UserScope{}, fmt.Errorf("resolve current-user acceptance scope: %w", err)
	}
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return UserScope{}, errors.New("acceptance endpoint directory must be a canonical absolute path")
	}
	relative, err := filepath.Rel(current.Directory(), directory)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return UserScope{}, errors.New("acceptance endpoint directory must be an isolated child of the current-user runtime directory")
	}
	address, err := acceptanceAddress(current.Transport(), directory)
	if err != nil {
		return UserScope{}, err
	}
	return newUserScope(directory, current.Transport(), address)
}

func acceptanceAddress(transport Transport, directory string) (string, error) {
	switch transport {
	case TransportUnixSocket:
		return filepath.Join(directory, acceptanceUnixSocketName), nil
	case TransportWindowsNamedPipe:
		digest := sha256.Sum256([]byte(directory))
		return `\\.\pipe\spice-agent-acceptance-` + hex.EncodeToString(digest[:]), nil
	default:
		return "", errors.New("acceptance endpoint transport is unsupported")
	}
}
