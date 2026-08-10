package tuisession

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

// RandomIdentifierSource produces cryptographically random identifiers without
// mutable package state.
type RandomIdentifierSource struct{}

// Next returns one lowercase 128-bit hexadecimal identifier.
func (RandomIdentifierSource) Next() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", fmt.Errorf("read random session identifier: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
