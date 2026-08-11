package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type opencodeSeededDefect struct{}

func (opencodeSeededDefect) Apply(repository opencodeRepository) ([]byte, error) {
	original, err := repository.Read(openCodeSeededDefectPath)
	if err != nil {
		return nil, err
	}
	needle := []byte(openCodeSeededDefectOriginal)
	replacement := []byte(openCodeSeededDefectReplacement)
	if bytes.Count(original, needle) != 1 || bytes.Contains(original, replacement) {
		return nil, errors.New("seeded defect source differs from the reviewed boundary")
	}
	mutated := bytes.Replace(original, needle, replacement, 1)
	path := filepath.Join(repository.target, filepath.FromSlash(openCodeSeededDefectPath))
	if err = os.WriteFile(path, mutated, 0o600); err != nil { // #nosec G306 -- disposable evaluation source is private.
		return nil, fmt.Errorf("write disposable seeded defect: %w", err)
	}
	return original, nil
}
