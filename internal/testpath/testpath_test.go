package testpath

import (
	"path/filepath"
	"testing"
)

func TestTempDirIsCanonicalAndExisting(t *testing.T) {
	directory := TempDir(t)
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		t.Fatalf("canonical temporary directory = %q", directory)
	}
	if resolved := Resolve(t, directory); resolved != directory {
		t.Fatalf("resolved temporary directory = %q, want %q", resolved, directory)
	}
}
