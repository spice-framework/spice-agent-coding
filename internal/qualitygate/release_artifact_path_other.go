//go:build !windows

package main

import (
	"errors"
	"path/filepath"
)

// releaseArtifactPath owns platform-native release artifact path validation.
type releaseArtifactPath struct{}

func (owner releaseArtifactPath) normalizeReleaseArtifactDirectory(directory string) (string, error) {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return "", errors.New("release artifact gate requires a canonical absolute -artifacts directory")
	}
	return directory, nil
}
