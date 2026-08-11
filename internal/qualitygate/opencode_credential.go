package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

type opencodeCredential struct{}

func (opencodeCredential) SourcePath(home string) string {
	if data := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(data) {
		return filepath.Join(data, "opencode", "auth.json")
	}
	return filepath.Join(home, ".local", "share", "opencode", "auth.json")
}

func (opencodeCredential) Copy(source, destination string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) {
		return errors.New("OpenRouter credential paths must be absolute")
	}
	content, err := os.ReadFile(source) // #nosec G304 -- explicit configured OpenCode credential source.
	if err != nil {
		return errors.New("read configured OpenRouter credential")
	}
	if len(content) == 0 || len(content) > 64<<10 {
		return errors.New("configured OpenCode credential store has an invalid size")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var store opencodeCredentialStore
	if err = decoder.Decode(&store); err != nil {
		return errors.New("decode configured OpenCode credential store")
	}
	raw, exists := store[openCodeProvider]
	if !exists {
		return errors.New("configured OpenCode credential store has no OpenRouter entry")
	}
	entryDecoder := json.NewDecoder(bytes.NewReader(raw))
	entryDecoder.DisallowUnknownFields()
	var entry opencodeCredentialEntry
	if err = entryDecoder.Decode(&entry); err != nil || entry.Type != openCodeCredentialType || !(opencodeCredential{}).validOpenCodeCredential(entry.Key) {
		return errors.New("configured OpenRouter credential is invalid")
	}
	isolated, err := json.Marshal(opencodeCredentialStore{openCodeProvider: raw})
	if err != nil {
		return errors.New("encode isolated OpenRouter credential")
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create isolated OpenRouter credential directory: %w", err)
	}
	if err = os.WriteFile(destination, append(isolated, '\n'), 0o600); err != nil { // #nosec G306 -- credential is owner-only.
		return errors.New("write isolated OpenRouter credential")
	}
	return nil
}

func (owner opencodeCredential) validOpenCodeCredential(value string) bool {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
