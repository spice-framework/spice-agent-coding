package testpath

import (
	"path/filepath"
	"testing"
)

// TempDir returns a test-owned temporary directory with operating-system
// symlinks resolved. This accounts for macOS's lexical /var to /private/var
// alias without weakening production path or symlink validation.
func TempDir(tb testing.TB) string {
	tb.Helper()
	return Resolve(tb, tb.TempDir())
}

// Resolve returns the canonical absolute spelling of an existing test path.
func Resolve(tb testing.TB, path string) string {
	tb.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		tb.Fatalf("resolve test path %q: %v", path, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		tb.Fatalf("make test path absolute %q: %v", resolved, err)
	}
	return filepath.Clean(resolved)
}
