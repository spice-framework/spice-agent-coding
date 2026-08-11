package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type opencodeWorkspace struct {
	root       string
	repository string
	config     string
	auth       string
}

func newOpenCodeWorkspace() (*opencodeWorkspace, error) {
	root, err := os.MkdirTemp("", "spice-opencode-evaluation-")
	if err != nil {
		return nil, fmt.Errorf("create OpenCode evaluation workspace: %w", err)
	}
	workspace := &opencodeWorkspace{
		root: root, repository: filepath.Join(root, "repository"), config: filepath.Join(root, "opencode.json"),
		auth: filepath.Join(root, "data", "opencode", "auth.json"),
	}
	for _, directory := range []string{
		workspace.repository, filepath.Join(root, "home"), filepath.Join(root, "config"), filepath.Join(root, "data"),
		filepath.Join(root, "cache"), filepath.Join(root, "state"), filepath.Join(root, "appdata"),
		filepath.Join(root, "localappdata"), filepath.Join(root, "tmp"), filepath.Join(root, "opencode-config"),
	} {
		if err = os.MkdirAll(directory, 0o700); err != nil {
			cleanupErr := os.RemoveAll(root)
			return nil, errors.Join(fmt.Errorf("create OpenCode evaluation directory: %w", err), cleanupErr)
		}
	}
	return workspace, nil
}

func (workspace *opencodeWorkspace) Close() error {
	if workspace == nil || workspace.root == "" {
		return nil
	}
	temporary := filepath.Clean(os.TempDir())
	root := filepath.Clean(workspace.root)
	relative, err := filepath.Rel(temporary, root)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		!strings.HasPrefix(filepath.Base(root), "spice-opencode-evaluation-") {
		return errors.New("refuse to remove unowned OpenCode evaluation workspace")
	}
	err = os.RemoveAll(root)
	if err == nil {
		workspace.root = ""
		workspace.repository = ""
		workspace.config = ""
		workspace.auth = ""
	}
	return err
}

func (workspace *opencodeWorkspace) CaseRepository(label string) (string, error) {
	if workspace == nil || workspace.root == "" || label == "" || strings.ContainsAny(label, "/\\\x00") {
		return "", errors.New("OpenCode case label is invalid")
	}
	directory := filepath.Join(workspace.root, "cases", label, "repository")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create OpenCode case repository: %w", err)
	}
	return directory, nil
}
