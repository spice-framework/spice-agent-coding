//go:build !windows

package main

import (
	"path/filepath"
	"testing"
)

func TestNormalizeReleaseArtifactDirectoryPreservesCanonicalAbsolutePath(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "verified subjects")
	if got, err := (releaseArtifactPath{}).normalizeReleaseArtifactDirectory(directory); err != nil || got != directory {
		t.Fatalf("normalized directory = %q, %v", got, err)
	}
}

func TestNormalizeReleaseArtifactDirectoryRejectsUnsafeUnixForms(t *testing.T) {
	t.Parallel()
	for _, directory := range []string{"relative/subjects", "/tmp/../escape", "/tmp//subjects"} {
		if got, err := (releaseArtifactPath{}).normalizeReleaseArtifactDirectory(directory); err == nil {
			t.Errorf("normalizeReleaseArtifactDirectory(%q) = %q, nil", directory, got)
		}
	}
}
