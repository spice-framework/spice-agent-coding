package client

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
)

const (
	// InitializationAttemptIDBytes is the exact protocol wire width of an
	// initialization attempt identity.
	InitializationAttemptIDBytes       = 16
	initializationAttemptBytes         = InitializationAttemptIDBytes
	initializationAttemptTextBytes     = initializationAttemptBytes * 2
	initializationAttemptGenerateTries = 4
	initializationAttemptProtocolMajor = uint32(1)
	initializationAttemptProtocolMinor = uint32(3)
)

// InitializationAttemptID is one immutable, comparable 128-bit identity for
// an exact initialization intent. Its canonical text form is 32 lowercase
// hexadecimal characters without separators.
type InitializationAttemptID struct {
	value [initializationAttemptBytes]byte
}

// NewInitializationAttemptID securely generates a nonzero initialization
// attempt identity. Callers own the returned identity and may reuse it only to
// replay the exact same InitializeRequest.
func NewInitializationAttemptID() (InitializationAttemptID, error) {
	return generateInitializationAttemptID(rand.Reader)
}

func generateInitializationAttemptID(source io.Reader) (InitializationAttemptID, error) {
	if source == nil {
		return InitializationAttemptID{}, errors.New("initialization attempt entropy source is unavailable")
	}
	for range initializationAttemptGenerateTries {
		var value InitializationAttemptID
		if _, err := io.ReadFull(source, value.value[:]); err != nil {
			return InitializationAttemptID{}, errors.New("initialization attempt entropy source failed")
		}
		if value.Validate() == nil {
			return value, nil
		}
	}
	return InitializationAttemptID{}, errors.New("initialization attempt entropy source produced only zero identities")
}

// ParseInitializationAttemptID parses the canonical lowercase hexadecimal
// representation of an initialization attempt identity.
func ParseInitializationAttemptID(encoded string) (InitializationAttemptID, error) {
	if len(encoded) != initializationAttemptTextBytes || encoded != stringLower(encoded) {
		return InitializationAttemptID{}, errors.New("initialization attempt ID must be 32 lowercase hexadecimal characters")
	}
	var value InitializationAttemptID
	if _, err := hex.Decode(value.value[:], []byte(encoded)); err != nil {
		return InitializationAttemptID{}, errors.New("initialization attempt ID must be 32 lowercase hexadecimal characters")
	}
	if err := value.Validate(); err != nil {
		return InitializationAttemptID{}, err
	}
	return value, nil
}

// Bytes returns the exact immutable wire representation by value.
func (id InitializationAttemptID) Bytes() [initializationAttemptBytes]byte { return id.value }

// String returns the canonical lowercase hexadecimal representation.
func (id InitializationAttemptID) String() string { return hex.EncodeToString(id.value[:]) }

// MarshalText implements encoding.TextMarshaler using the canonical form.
func (id InitializationAttemptID) MarshalText() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return []byte(id.String()), nil
}

// Validate rejects the zero or corrupted initialization attempt identity.
func (id InitializationAttemptID) Validate() error {
	if id == (InitializationAttemptID{}) {
		return errors.New("initialization attempt ID must be nonzero")
	}
	return nil
}

func stringLower(value string) string {
	for _, current := range value {
		if current >= 'A' && current <= 'Z' {
			return ""
		}
	}
	return value
}
