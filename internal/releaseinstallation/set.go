package releaseinstallation

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Set is a completely validated nine-subject distribution set.
type Set struct {
	metadata releaseMetadata
	archives map[string]string
	digests  map[string]string
}

// Version returns the exact release version, including its v prefix.
func (set *Set) Version() string {
	if set == nil {
		return ""
	}
	return set.metadata.Version
}

// Commit returns the exact 40-character source commit embedded in the release.
func (set *Set) Commit() string {
	if set == nil {
		return ""
	}
	return set.metadata.Commit
}

// ExtractNative extracts the validated archive for goos/goarch beneath a new,
// caller-owned directory and returns the directory containing both binaries.
func (set *Set) ExtractNative(destination, goos, goarch string) (string, error) {
	if set == nil {
		return "", errors.New("release subject set is unavailable")
	}
	if destination == "" || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return "", errors.New("release extraction directory must be canonical and absolute")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return "", errors.New("release extraction directory already exists")
		}
		return "", fmt.Errorf("inspect release extraction directory: %w", err)
	}
	archivePath, target, err := set.nativeArchive(goos, goarch)
	if err != nil {
		return "", err
	}
	if err = os.Mkdir(destination, 0o700); err != nil {
		return "", fmt.Errorf("create release extraction directory: %w", err)
	}
	installRoot := filepath.Join(destination, (archiveInspector{}).archiveRoot(target.Archive))
	err = (archiveInspector{}).inspectArchive(archivePath, target, set.metadata.Payloads, func(
		relative string, mode fs.FileMode, reader io.Reader,
	) error {
		return set.extractFile(installRoot, relative, mode, reader)
	})
	if err != nil {
		// #nosec G703 -- destination was validated, required absent, and created by this call.
		return "", errors.Join(
			fmt.Errorf("extract native release archive: %w", err),
			os.RemoveAll(destination),
		)
	}
	return installRoot, nil
}

func (set *Set) nativeArchive(goos, goarch string) (string, releaseTarget, error) {
	if set == nil || set.archives == nil {
		return "", releaseTarget{}, errors.New("release subject set is unavailable")
	}
	archivePath, found := set.archives[goos+"/"+goarch]
	if !found {
		return "", releaseTarget{}, fmt.Errorf("release has no archive for %s/%s", goos, goarch)
	}
	target, found := (archiveInspector{}).findTarget(set.metadata.Targets, goos, goarch)
	if !found {
		return "", releaseTarget{}, errors.New("release target metadata is unavailable")
	}
	if err := (Verifier{}).verifyFile(archivePath, set.digests[target.Archive]); err != nil {
		return "", releaseTarget{}, fmt.Errorf("revalidate native release archive: %w", err)
	}
	return archivePath, target, nil
}

func (set *Set) extractFile(root, relative string, mode fs.FileMode, reader io.Reader) error {
	file := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return err
	}
	opened, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm()) // #nosec G304 -- relative is exact validated archive membership.
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(opened, reader)
	closeErr := opened.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr == nil {
		copyErr = os.Chmod(file, mode.Perm())
	}
	return copyErr
}
