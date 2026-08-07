package endpoint

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// TokenBytes is the exact entropy size of a local-daemon credential.
	TokenBytes = 32
	// BearerPrefix is the canonical authorization scheme and spacing.
	BearerPrefix = "Bearer "

	tokenAttempts = 4
)

// Token is one opaque 256-bit current-user daemon credential. It is not a
// snapshot authority key and must never enter payloads, logs, events, generated
// files, or errors. Indirection prevents fmt's pointer fallback from exposing
// credential bytes.
type Token struct {
	state *tokenState
	notComparable
}

type (
	tokenState    struct{ value [TokenBytes]byte }
	notComparable [0]func()
)

// GenerateToken creates a credential from the operating-system CSPRNG.
func GenerateToken() (Token, error) { return generateToken(rand.Reader) }

func generateToken(random io.Reader) (Token, error) {
	if random == nil {
		return Token{}, errors.New("endpoint token randomness is nil")
	}
	for range tokenAttempts {
		token := Token{state: &tokenState{}}
		if _, err := io.ReadFull(random, token.state.value[:]); err != nil {
			// Entropy-source details are deliberately omitted so a faulty custom
			// reader cannot place credential material in logs through its error.
			return Token{}, errors.New("generate endpoint token")
		}
		if token.valid() {
			return token, nil
		}
	}
	return Token{}, errors.New("generate nonzero endpoint token")
}

// ParseToken decodes the canonical unpadded base64url credential form.
func ParseToken(encoded string) (Token, error) {
	if len(encoded) != base64.RawURLEncoding.EncodedLen(TokenBytes) ||
		strings.TrimSpace(encoded) != encoded {
		return Token{}, errors.New("endpoint token encoding is invalid")
	}
	token := Token{state: &tokenState{}}
	written, err := base64.RawURLEncoding.Decode(token.state.value[:], []byte(encoded))
	if err != nil || written != TokenBytes || !token.valid() ||
		base64.RawURLEncoding.EncodeToString(token.state.value[:]) != encoded {
		return Token{}, errors.New("endpoint token encoding is invalid")
	}
	return token, nil
}

// ParseAuthorizationValue validates the exact canonical Bearer representation.
func ParseAuthorizationValue(value string) (Token, error) {
	if !strings.HasPrefix(value, BearerPrefix) ||
		len(value) != len(BearerPrefix)+base64.RawURLEncoding.EncodedLen(TokenBytes) {
		return Token{}, errors.New("bearer credential is invalid")
	}
	return ParseToken(strings.TrimPrefix(value, BearerPrefix))
}

// AuthorizationValue returns the explicit Bearer value stored in user-only
// endpoint metadata and sent as transport metadata.
func (token Token) AuthorizationValue() (string, error) {
	if !token.valid() {
		return "", errors.New("endpoint token is invalid")
	}
	return BearerPrefix + base64.RawURLEncoding.EncodeToString(token.state.value[:]), nil
}

// Validate reports whether the token contains one nonzero credential.
func (token Token) Validate() error {
	if !token.valid() {
		return errors.New("endpoint token is invalid")
	}
	return nil
}

// Equal compares two valid credentials in constant time.
func (token Token) Equal(other Token) bool {
	if !token.valid() || !other.valid() {
		return false
	}
	return subtle.ConstantTimeCompare(token.state.value[:], other.state.value[:]) == 1
}

// String prevents accidental formatting from exposing credential bytes.
func (Token) String() string { return "[REDACTED endpoint token]" }

// GoString prevents %#v formatting from exposing representation details.
func (Token) GoString() string { return "endpoint.Token([REDACTED])" }

// MarshalJSON makes accidental serialization visibly redacted.
func (Token) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED endpoint token]"`), nil
}

// Format prevents every fmt verb, flag, width, and precision from reflecting
// the private byte array.
func (Token) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[REDACTED endpoint token]")
}

func (token Token) valid() bool {
	_ = token.notComparable
	if token.state == nil {
		return false
	}
	var zero [TokenBytes]byte
	return subtle.ConstantTimeCompare(token.state.value[:], zero[:]) == 0
}
