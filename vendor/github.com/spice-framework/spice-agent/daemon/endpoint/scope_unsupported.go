//go:build !linux && !darwin && !windows

package endpoint

import "errors"

func currentUserScope() (UserScope, error) {
	return UserScope{}, errors.New("current-user endpoint scope is unsupported on this platform")
}

func validateScopePlatform(UserScope) error {
	return errors.New("current-user endpoint scope is unsupported on this platform")
}
