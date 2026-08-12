package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
)

// Fingerprint is the stable identity of one canonical absolute workspace.
type Fingerprint string

// NewFingerprint derives the stable SHA-256 identity of an absolute workspace.
func NewFingerprint(root string) (Fingerprint, error) {
	cleaned := filepath.Clean(root)
	if root == "" || cleaned == "." || !filepath.IsAbs(cleaned) {
		return "", errors.New("workspace fingerprint requires an absolute root")
	}
	digest := sha256.Sum256([]byte(cleaned))
	return Fingerprint("sha256:" + hex.EncodeToString(digest[:])), nil
}

// String returns the fingerprint's protocol value.
func (fingerprint Fingerprint) String() string {
	return string(fingerprint)
}
