package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
)

func TestNewFingerprintPreservesCanonicalAbsoluteWorkspaceIdentity(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "workspace", "..", "workspace")
	cleaned := filepath.Clean(root)
	first, err := NewFingerprint(root)
	if err != nil {
		t.Fatalf("NewFingerprint() error = %v", err)
	}
	second, err := NewFingerprint(cleaned)
	if err != nil {
		t.Fatalf("NewFingerprint(cleaned) error = %v", err)
	}
	digest := sha256.Sum256([]byte(cleaned))
	want := "sha256:" + hex.EncodeToString(digest[:])
	if first != second || first.String() != want {
		t.Fatalf("fingerprints = %q, %q, want %q", first, second, want)
	}
}

func TestNewFingerprintRejectsNonAbsoluteWorkspaceRoots(t *testing.T) {
	t.Parallel()

	for _, root := range []string{"", ".", "relative", filepath.Join("relative", "workspace")} {
		t.Run(root, func(t *testing.T) {
			t.Parallel()
			if fingerprint, err := NewFingerprint(root); err == nil || fingerprint != "" {
				t.Fatalf("NewFingerprint(%q) = %q, %v", root, fingerprint, err)
			}
		})
	}
}
