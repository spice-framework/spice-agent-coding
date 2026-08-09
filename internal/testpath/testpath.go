package testpath

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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

// Environment returns the current process environment with each override
// represented exactly once. Avoiding duplicate keys is required on Windows,
// where child-process selection among differently cased or repeated entries is
// not a useful test contract.
func Environment(overrides map[string]string) []string {
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]string, 0, len(os.Environ())+len(keys))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !environmentOverridden(name, keys) {
			result = append(result, entry)
		}
	}
	for _, key := range keys {
		result = append(result, key+"="+overrides[key])
	}
	return result
}

func environmentOverridden(candidate string, keys []string) bool {
	for _, key := range keys {
		if candidate == key || runtime.GOOS == "windows" && strings.EqualFold(candidate, key) {
			return true
		}
	}
	return false
}
