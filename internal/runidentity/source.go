// Package runidentity supplies the distribution's cryptographically random
// agent identity source without global application state.
package runidentity

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"sync"
)

const (
	entropyBytes       = 24
	maximumPrefixBytes = 32
)

// Source emits canonical URL-safe identities with 192 bits of entropy.
// The reader is serialized so injected deterministic readers remain safe when
// the engine allocates identities concurrently.
type Source struct {
	reader io.Reader
	mu     sync.Mutex
}

// New constructs a source from an explicit entropy reader.
func New(reader io.Reader) (*Source, error) {
	if reader == nil {
		return nil, errors.New("agent ID source requires an entropy reader")
	}
	return &Source{reader: reader}, nil
}

// NewCrypto constructs the production source backed by crypto/rand.
func NewCrypto() *Source {
	return &Source{reader: rand.Reader}
}

// Next returns prefix plus a 32-character unpadded base64url token. Prefixes
// are intentionally narrow so every complete ID is bounded and canonical.
func (source *Source) Next(prefix string) (string, error) {
	if source == nil || source.reader == nil {
		return "", errors.New("agent ID source is unavailable")
	}
	if !validPrefix(prefix) {
		return "", errors.New("agent ID prefix is not canonical")
	}
	entropy := make([]byte, entropyBytes)
	source.mu.Lock()
	_, err := io.ReadFull(source.reader, entropy)
	source.mu.Unlock()
	if err != nil {
		return "", errors.New("generate agent ID entropy")
	}
	return prefix + "-" + base64.RawURLEncoding.EncodeToString(entropy), nil
}

func validPrefix(prefix string) bool {
	if len(prefix) == 0 || len(prefix) > maximumPrefixBytes || prefix[0] < 'a' || prefix[0] > 'z' || prefix[len(prefix)-1] == '-' {
		return false
	}
	previousHyphen := false
	for _, character := range []byte(prefix) {
		hyphen := character == '-'
		lowercase := character >= 'a' && character <= 'z'
		digit := character >= '0' && character <= '9'
		if !lowercase && !digit && !hyphen {
			return false
		}
		if hyphen && previousHyphen {
			return false
		}
		previousHyphen = hyphen
	}
	return true
}
