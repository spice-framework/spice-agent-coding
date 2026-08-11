//go:build windows

package main

import (
	"errors"
	"path/filepath"
	"strings"
)

// releaseArtifactPath owns platform-native release artifact path validation.
type releaseArtifactPath struct{}

func (owner releaseArtifactPath) normalizeReleaseArtifactDirectory(directory string) (string, error) {
	if directory == "" || strings.IndexByte(directory, 0) >= 0 {
		return "", errors.New("release artifact gate requires a canonical absolute -artifacts directory")
	}
	normalized := filepath.FromSlash(directory)
	volume := filepath.VolumeName(normalized)
	if len(volume) != 2 || volume[1] != ':' || !(releaseArtifactPath{}).isASCIILetter(volume[0]) ||
		!filepath.IsAbs(normalized) || strings.HasPrefix(normalized, `\\`) {
		return "", errors.New("release artifact gate requires a local-drive absolute -artifacts directory")
	}
	cleaned := filepath.Clean(normalized)
	if cleaned != normalized || cleaned == volume+`\` {
		return "", errors.New("release artifact gate requires a canonical traversal-free -artifacts directory")
	}
	return cleaned, nil
}

func (owner releaseArtifactPath) isASCIILetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}
