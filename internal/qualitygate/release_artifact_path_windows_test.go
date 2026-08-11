//go:build windows

package main

import (
	"path/filepath"
	"testing"
)

func TestNormalizeReleaseArtifactDirectoryAcceptsRunnerMixedSeparators(t *testing.T) {
	t.Parallel()
	want := `D:\a\_temp\go-distribution-release-verified`
	got, err := (releaseArtifactPath{}).normalizeReleaseArtifactDirectory(`D:\a\_temp/go-distribution-release-verified`)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("normalized directory = %q, want %q", got, want)
	}
	canonical := filepath.Join(t.TempDir(), "verified subjects")
	if got, err = (releaseArtifactPath{}).normalizeReleaseArtifactDirectory(canonical); err != nil || got != canonical {
		t.Fatalf("canonical directory = %q, %v", got, err)
	}
}

func TestNormalizeReleaseArtifactDirectoryRejectsUnsafeWindowsForms(t *testing.T) {
	t.Parallel()
	for _, directory := range []string{
		`relative\subjects`,
		`D:\a\..\escape`,
		`D:\a\.\subjects`,
		`\\server\share\subjects`,
		`\\?\D:\subjects`,
		`\rooted-without-drive`,
		`D:\`,
	} {
		if got, err := (releaseArtifactPath{}).normalizeReleaseArtifactDirectory(directory); err == nil {
			t.Errorf("normalizeReleaseArtifactDirectory(%q) = %q, nil", directory, got)
		}
	}
}
