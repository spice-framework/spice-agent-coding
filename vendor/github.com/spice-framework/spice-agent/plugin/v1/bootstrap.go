package pluginv1

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// BootstrapMaximumBytes is the maximum encoded launch bootstrap record.
	// The bound includes the terminating line feed.
	BootstrapMaximumBytes = 4096

	// ReadinessRecord is the only record a runtime plugin writes to stdout.
	// After writing this record, a plugin must keep stdout silent.
	ReadinessRecord = "{\"ready\":true}\n"
)

type bootstrapRecord struct {
	Address string `json:"address"`
	Secret  string `json:"secret"`
}

// EncodeBootstrap returns the deterministic, newline-terminated JSON launch
// record written to a plugin's private stdin. Secret must contain exactly
// HandshakeSecretBytes bytes. The caller retains ownership of secret and is
// responsible for clearing it when it is no longer needed.
//
// Errors intentionally do not include the address or secret.
func EncodeBootstrap(address string, secret []byte) ([]byte, error) {
	if err := validateBootstrapAddress(address); err != nil {
		return nil, err
	}
	if len(secret) != HandshakeSecretBytes {
		return nil, errors.New("plugin bootstrap secret has an invalid size")
	}
	encoded, err := json.Marshal(bootstrapRecord{
		Address: address,
		Secret:  base64.RawURLEncoding.EncodeToString(secret),
	})
	if err != nil {
		return nil, errors.New("encode plugin bootstrap")
	}
	encoded = append(encoded, '\n')
	if len(encoded) > BootstrapMaximumBytes {
		return nil, errors.New("plugin bootstrap exceeds its byte limit")
	}
	return encoded, nil
}

// DecodeBootstrap reads exactly one bounded, newline-terminated JSON launch
// record. The returned secret is newly allocated and owned by the caller. The
// caller must clear it after constructing the authenticated plugin service.
//
// Errors intentionally do not include untrusted input, the address, or secret.
func DecodeBootstrap(input io.Reader) (string, []byte, error) {
	if input == nil {
		return "", nil, errors.New("plugin bootstrap input is required")
	}
	limited := &io.LimitedReader{R: input, N: BootstrapMaximumBytes + 1}
	encoded, err := io.ReadAll(limited)
	if err != nil {
		return "", nil, errors.New("read plugin bootstrap")
	}
	if len(encoded) > BootstrapMaximumBytes {
		return "", nil, errors.New("plugin bootstrap exceeds its byte limit")
	}
	if !utf8.Valid(encoded) {
		return "", nil, errors.New("plugin bootstrap is not valid UTF-8")
	}
	if len(encoded) == 0 || encoded[len(encoded)-1] != '\n' || bytes.Count(encoded, []byte{'\n'}) != 1 {
		return "", nil, errors.New("plugin bootstrap must contain exactly one line")
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded[:len(encoded)-1]))
	var record strictBootstrapRecord
	if err = decoder.Decode(&record); err != nil {
		return "", nil, errors.New("decode plugin bootstrap")
	}
	var trailing json.RawMessage
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", nil, errors.New("plugin bootstrap contains trailing data")
	}
	if err = validateBootstrapAddress(record.Address); err != nil {
		return "", nil, err
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(record.Secret)
	if err != nil || len(secret) != HandshakeSecretBytes {
		clear(secret)
		return "", nil, errors.New("plugin bootstrap secret is invalid")
	}
	return record.Address, secret, nil
}

// WriteReadiness writes the exact runtime-plugin readiness record. When output
// implements Flush() error, WriteReadiness flushes it before returning.
func WriteReadiness(output io.Writer) error {
	if output == nil {
		return errors.New("plugin readiness output is required")
	}
	written, err := io.WriteString(output, ReadinessRecord)
	if err != nil || written != len(ReadinessRecord) {
		return errors.New("write plugin readiness")
	}
	if flusher, ok := output.(interface{ Flush() error }); ok {
		if err = flusher.Flush(); err != nil {
			return errors.New("flush plugin readiness")
		}
	}
	return nil
}

type strictBootstrapRecord bootstrapRecord

func (record *strictBootstrapRecord) UnmarshalJSON(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return errors.New("bootstrap record must be an object")
	}
	seenAddress := false
	seenSecret := false
	for decoder.More() {
		key, decodeErr := decoder.Token()
		if decodeErr != nil {
			return errors.New("bootstrap record key is invalid")
		}
		name, ok := key.(string)
		if !ok {
			return errors.New("bootstrap record key is invalid")
		}
		switch name {
		case "address":
			if seenAddress {
				return errors.New("bootstrap record contains a duplicate field")
			}
			seenAddress = true
			if decodeErr = decoder.Decode(&record.Address); decodeErr != nil {
				return errors.New("bootstrap record address is invalid")
			}
		case "secret":
			if seenSecret {
				return errors.New("bootstrap record contains a duplicate field")
			}
			seenSecret = true
			if decodeErr = decoder.Decode(&record.Secret); decodeErr != nil {
				return errors.New("bootstrap record secret is invalid")
			}
		default:
			return errors.New("bootstrap record contains an unknown field")
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return errors.New("bootstrap record is incomplete")
	}
	if !seenAddress || !seenSecret {
		return errors.New("bootstrap record is missing a required field")
	}
	if decoder.More() {
		return errors.New("bootstrap record contains trailing data")
	}
	return nil
}

func validateBootstrapAddress(address string) error {
	if address == "" || strings.TrimSpace(address) != address {
		return errors.New("plugin bootstrap address is invalid")
	}
	if len(address) > BootstrapMaximumBytes || !utf8.ValidString(address) || strings.IndexByte(address, 0) >= 0 {
		return errors.New("plugin bootstrap address is invalid")
	}
	for _, character := range address {
		if unicode.IsControl(character) {
			return errors.New("plugin bootstrap address is invalid")
		}
	}
	return nil
}
