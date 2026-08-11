package logging

import (
	"crypto/subtle"
	"errors"
	"fmt"
)

const correlationKeyBytes = 32

// Config owns the bounded Agent logging mailbox and optional readiness impact.
// A zero CorrelationKey requests fresh process-local random key material.
type Config struct {
	MailboxCapacity int
	IncludeProgress bool
	ReadinessImpact bool
	CorrelationKey  CorrelationKey
}

// DefaultConfig returns conservative secret-safe production defaults.
func DefaultConfig() Config {
	return Config{MailboxCapacity: 1024}
}

// Validate rejects ambiguous or unbounded configuration.
func (config Config) Validate() error {
	if config.MailboxCapacity < 1 || config.MailboxCapacity > 65536 {
		return errors.New("agent logging mailbox capacity must be between 1 and 65536")
	}
	return config.CorrelationKey.validateOptional()
}

// CorrelationKey is immutable HMAC material with no material accessor.
type CorrelationKey struct {
	material [correlationKeyBytes]byte
	set      bool
}

// Format redacts key material under every fmt verb.
func (key CorrelationKey) Format(state fmt.State, _ rune) {
	value := "[PROCESS-LOCAL]"
	if key.set {
		value = "[REDACTED]"
	}
	_, _ = state.Write([]byte(value))
}

// NewCorrelationKey defensively copies exact 256-bit application-owned key
// material. Callers must treat the input as secret configuration.
func NewCorrelationKey(material []byte) (CorrelationKey, error) {
	if len(material) != correlationKeyBytes {
		return CorrelationKey{}, fmt.Errorf("agent logging correlation key must contain %d bytes", correlationKeyBytes)
	}
	var result CorrelationKey
	copy(result.material[:], material)
	result.set = true
	if err := result.validateOptional(); err != nil {
		return CorrelationKey{}, err
	}
	return result, nil
}

func (key CorrelationKey) validateOptional() error {
	zero := make([]byte, correlationKeyBytes)
	if !key.set {
		if subtle.ConstantTimeCompare(key.material[:], zero) != 1 {
			return errors.New("agent logging correlation key is invalid")
		}
		return nil
	}
	if subtle.ConstantTimeCompare(key.material[:], zero) == 1 {
		return errors.New("agent logging correlation key must not be all zero")
	}
	return nil
}
