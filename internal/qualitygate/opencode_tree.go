package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type opencodeTree struct{}

func (opencodeTree) Snapshot(root string) (opencodeTreeSnapshot, error) {
	if !filepath.IsAbs(root) {
		return opencodeTreeSnapshot{}, errors.New("OpenCode tree root must be absolute")
	}
	paths, err := (opencodeTree{}).inventoryOpenCodeTree(root)
	if err != nil {
		return opencodeTreeSnapshot{}, err
	}
	return (opencodeTree{}).digestOpenCodeTree(root, paths)
}

func (owner opencodeTree) inventoryOpenCodeTree(root string) ([]string, error) {
	paths := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("OpenCode evaluation tree contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() || info.Size() > maximumOpenCodeRepositoryFileBytes {
			return errors.New("OpenCode evaluation tree contains an unsupported file")
		}
		relative, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil {
			return relativeErr
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("inventory OpenCode evaluation tree: %w", err)
	}
	slices.Sort(paths)
	return paths, nil
}

func (owner opencodeTree) digestOpenCodeTree(root string, paths []string) (opencodeTreeSnapshot, error) {
	treeDigest := sha256.New()
	files := make(map[string][sha256.Size]byte, len(paths))
	for _, relative := range paths {
		file, openErr := os.Open(filepath.Join(root, filepath.FromSlash(relative))) // #nosec G304 -- relative path came from the owned tree walk.
		if openErr != nil {
			return opencodeTreeSnapshot{}, fmt.Errorf("open OpenCode evaluation file: %w", openErr)
		}
		fileDigest := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(treeDigest, fileDigest), file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return opencodeTreeSnapshot{}, errors.Join(copyErr, closeErr)
		}
		encoded := fileDigest.Sum(nil)
		var fixed [sha256.Size]byte
		copy(fixed[:], encoded)
		files[relative] = fixed
		if _, writeErr := io.WriteString(treeDigest, "\x00"+relative+"\x00"); writeErr != nil {
			return opencodeTreeSnapshot{}, fmt.Errorf("hash OpenCode evaluation path: %w", writeErr)
		}
	}
	var digest [sha256.Size]byte
	copy(digest[:], treeDigest.Sum(nil))
	return opencodeTreeSnapshot{Digest: digest, Files: files}, nil
}

func (opencodeTree) ContainsUnsafePath(relative string) bool {
	normalized := strings.ToLower(filepath.ToSlash(relative))
	base := strings.ToLower(filepath.Base(normalized))
	if normalized == ".opencode" || strings.HasPrefix(normalized, ".opencode/") ||
		base == "auth.json" || base == "opencode.json" || base == "opencode.jsonc" ||
		base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	return slices.Contains([]string{".key", ".p12", ".pfx", ".pem"}, filepath.Ext(base))
}
