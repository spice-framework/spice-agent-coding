package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type opencodeRepository struct {
	source string
	target string
	tree   opencodeTree
}

func newOpenCodeRepository(source, target string) (opencodeRepository, error) {
	if !filepath.IsAbs(source) || !filepath.IsAbs(target) || filepath.Clean(source) == filepath.Clean(target) {
		return opencodeRepository{}, errors.New("OpenCode repository copy requires distinct absolute roots")
	}
	return opencodeRepository{source: filepath.Clean(source), target: filepath.Clean(target), tree: opencodeTree{}}, nil
}

func (repository opencodeRepository) Copy(ctx context.Context) error {
	environment := minimumEvaluationEnvironment(repository.target)
	status, err := boundedCommandOutput(
		ctx, repository.source, environment, maximumOpenCodeInventoryBytes,
		"git", "status", "--porcelain=v1", "-z", "--untracked-files=all",
	)
	if err != nil {
		return fmt.Errorf("inspect OpenCode source repository: %w", err)
	}
	if status != "" {
		return errors.New("OpenCode evaluation refuses a dirty source repository")
	}
	inventory, err := boundedCommandOutput(
		ctx, repository.source, environment, maximumOpenCodeInventoryBytes,
		"git", "ls-files", "-z", "--cached",
	)
	if err != nil {
		return fmt.Errorf("inventory OpenCode source repository: %w", err)
	}
	paths := strings.Split(inventory, "\x00")
	total := int64(0)
	copied := 0
	for _, relative := range paths {
		if relative == "" {
			continue
		}
		if err = repository.copyFile(relative, &total); err != nil {
			return err
		}
		copied++
	}
	if copied == 0 || total > maximumOpenCodeRepositoryBytes {
		return errors.New("OpenCode repository copy is empty or oversized")
	}
	return nil
}

func (repository opencodeRepository) copyFile(relative string, total *int64) error {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if unsafeOpenCodeCopyPath(clean, relative, repository.tree) {
		return errors.New("OpenCode source repository contains an unsafe tracked path")
	}
	source := filepath.Join(repository.source, clean)
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect OpenCode source file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maximumOpenCodeRepositoryFileBytes {
		return errors.New("OpenCode source repository contains an unsupported tracked file")
	}
	*total += info.Size()
	if *total > maximumOpenCodeRepositoryBytes {
		return errors.New("OpenCode source repository exceeds its copy bound")
	}
	destination := filepath.Join(repository.target, clean)
	if err = os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create OpenCode repository directory: %w", err)
	}
	return copyOpenCodeFile(source, destination, info.Size())
}

func unsafeOpenCodeCopyPath(clean, relative string, tree opencodeTree) bool {
	return clean == "." || filepath.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) || tree.ContainsUnsafePath(relative)
}

func copyOpenCodeFile(source, destination string, size int64) error {
	input, err := os.Open(source) // #nosec G304 -- source is a validated tracked path.
	if err != nil {
		return fmt.Errorf("open OpenCode source file: %w", err)
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- destination is under the owned root.
	if err != nil {
		return errors.Join(fmt.Errorf("create OpenCode repository file: %w", err), input.Close())
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, size+1))
	closeErr := errors.Join(input.Close(), output.Close())
	if copyErr != nil || closeErr != nil || written != size {
		return errors.Join(errors.New("copy complete OpenCode repository file"), copyErr, closeErr)
	}
	return nil
}

func (repository opencodeRepository) Read(relative string) ([]byte, error) {
	if repository.tree.ContainsUnsafePath(relative) {
		return nil, errors.New("OpenCode repository read path is unsafe")
	}
	content, err := os.ReadFile(filepath.Join(repository.target, filepath.FromSlash(relative))) // #nosec G304 -- caller supplies a reviewed relative path.
	if err != nil {
		return nil, fmt.Errorf("read OpenCode repository file: %w", err)
	}
	return bytes.Clone(content), nil
}
