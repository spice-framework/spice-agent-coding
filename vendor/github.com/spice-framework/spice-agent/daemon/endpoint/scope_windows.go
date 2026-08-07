//go:build windows

package endpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

const windowsScopeDirectoryName = "spice-agent"

func currentUserScope() (UserScope, error) {
	localAppData, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return UserScope{}, fmt.Errorf("resolve current-user LocalAppData: %w", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return UserScope{}, fmt.Errorf("read current user for endpoint scope: %w", err)
	}
	if user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return UserScope{}, errors.New("current user SID is unavailable")
	}
	return currentUserWindowsScope(localAppData, user.User.Sid.String())
}

func currentUserWindowsScope(localAppData, sidText string) (UserScope, error) {
	if localAppData == "" || !filepath.IsAbs(localAppData) ||
		filepath.Clean(localAppData) != localAppData {
		return UserScope{}, errors.New("current-user LocalAppData must be a canonical absolute path")
	}
	sid, err := windows.StringToSid(sidText)
	if err != nil || sid == nil || !sid.IsValid() || sid.String() != sidText {
		return UserScope{}, errors.New("current user SID is invalid")
	}
	digest := sha256.Sum256([]byte(sidText))
	suffix := "user-" + hex.EncodeToString(digest[:])
	directory := filepath.Join(localAppData, windowsScopeDirectoryName, "runtime")
	address := `\\.\pipe\spice-agent-` + suffix
	return newUserScope(directory, TransportWindowsNamedPipe, address)
}

func validateScopePlatform(scope UserScope) error {
	if scope.transport != TransportWindowsNamedPipe {
		return errors.New("current-user endpoint scope requires a Windows named pipe")
	}
	return nil
}
