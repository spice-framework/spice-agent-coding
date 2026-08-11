package releaseinstallation

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"slices"
	"strings"
)

// archiveInspector owns archive structure, membership, mode, size, and digest validation.
type archiveInspector struct{}

func (inspector archiveInspector) inspectArchive(
	file string,
	target releaseTarget,
	payloads []releaseFile,
	consume func(string, fs.FileMode, io.Reader) error,
) error {
	if strings.HasSuffix(file, ".zip") {
		return inspector.inspectZip(file, target, payloads, consume)
	}
	if strings.HasSuffix(file, ".tar.gz") {
		return inspector.inspectTar(file, target, payloads, consume)
	}
	return errors.New("release archive extension is unsupported")
}

func (inspector archiveInspector) inspectZip(file string, target releaseTarget, payloads []releaseFile, consume func(string, fs.FileMode, io.Reader) error) error {
	archive, err := zip.OpenReader(file) // #nosec G304 -- exact checksummed archive subject.
	if err != nil {
		return err
	}
	defer archive.Close() //nolint:errcheck // Read-only close cannot affect validation.
	seen := make(map[string]struct{})
	for _, entry := range archive.File {
		if entry.Flags&1 != 0 {
			return errors.New("encrypted archive entry is forbidden")
		}
		if entry.UncompressedSize64 > math.MaxInt64 {
			return fmt.Errorf("archive entry %s exceeds the supported size", entry.Name)
		}
		opened, openErr := entry.Open()
		if openErr != nil {
			return openErr
		}
		entryErr := inspector.inspectEntry(entry.Name, entry.Mode(), int64(entry.UncompressedSize64), opened, target, payloads, seen, consume)
		closeErr := opened.Close()
		if entryErr != nil {
			return entryErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return inspector.validateArchiveMembership(target, payloads, seen)
}

func (inspector archiveInspector) inspectTar(file string, target releaseTarget, payloads []releaseFile, consume func(string, fs.FileMode, io.Reader) error) error {
	opened, err := os.Open(file) // #nosec G304 -- exact checksummed archive subject.
	if err != nil {
		return err
	}
	defer opened.Close() //nolint:errcheck // Read-only close cannot affect validation.
	compressed, err := gzip.NewReader(opened)
	if err != nil {
		return err
	}
	defer compressed.Close() //nolint:errcheck // Read-only close cannot affect validation.
	reader := tar.NewReader(compressed)
	seen := make(map[string]struct{})
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		if header.Typeflag != tar.TypeReg || header.Uid != 0 || header.Gid != 0 {
			return fmt.Errorf("archive entry %s is not a root-owned regular file", header.Name)
		}
		if header.Mode < 0 || header.Mode > math.MaxUint32 {
			return fmt.Errorf("archive entry %s mode is outside the supported range", header.Name)
		}
		if err = inspector.inspectEntry(header.Name, fs.FileMode(uint32(header.Mode)), header.Size, reader, target, payloads, seen, consume); err != nil {
			return err
		}
	}
	return inspector.validateArchiveMembership(target, payloads, seen)
}

func (inspector archiveInspector) inspectEntry(
	name string,
	mode fs.FileMode,
	size int64,
	reader io.Reader,
	target releaseTarget,
	payloads []releaseFile,
	seen map[string]struct{},
	consume func(string, fs.FileMode, io.Reader) error,
) error {
	relative, payload, isPayload, wantMode, err := inspector.validateArchiveEntry(name, mode, size, target, payloads, seen)
	if err != nil {
		return err
	}
	return inspector.consumeArchiveEntry(relative, size, payload, isPayload, wantMode, reader, consume)
}

func (inspector archiveInspector) validateArchiveEntry(
	name string,
	mode fs.FileMode,
	size int64,
	target releaseTarget,
	payloads []releaseFile,
	seen map[string]struct{},
) (string, releaseFile, bool, fs.FileMode, error) {
	relative, err := inspector.validateArchivePath(name, inspector.archiveRoot(target.Archive), seen)
	if err != nil {
		return "", releaseFile{}, false, 0, err
	}
	payload, isPayload := inspector.findPayload(payloads, relative)
	isBinary := slices.Contains(target.Binaries, relative)
	if !isPayload && !isBinary {
		return "", releaseFile{}, false, 0, fmt.Errorf("archive entry %s is not declared", relative)
	}
	wantMode := fs.FileMode(0o644)
	if isBinary {
		wantMode = 0o755
	}
	if !mode.IsRegular() || mode.Perm() != wantMode {
		return "", releaseFile{}, false, 0, fmt.Errorf("archive entry %s mode is %s, want %s", relative, mode, wantMode)
	}
	if size <= 0 || size > maximumEntry || isPayload && size != payload.Size {
		return "", releaseFile{}, false, 0, fmt.Errorf("archive entry %s size %d is invalid", relative, size)
	}
	return relative, payload, isPayload, wantMode, nil
}

func (inspector archiveInspector) validateArchivePath(name, root string, seen map[string]struct{}) (string, error) {
	if strings.Contains(name, "\\") || path.Clean(name) != name || !strings.HasPrefix(name, root+"/") {
		return "", fmt.Errorf("archive path %q is invalid", name)
	}
	relative := strings.TrimPrefix(name, root+"/")
	if relative == "" || strings.HasPrefix(relative, "/") || strings.HasPrefix(relative, "../") {
		return "", fmt.Errorf("archive path %q escapes its root", name)
	}
	if _, duplicate := seen[relative]; duplicate {
		return "", fmt.Errorf("archive entry %s is duplicated", relative)
	}
	seen[relative] = struct{}{}
	return relative, nil
}

func (inspector archiveInspector) consumeArchiveEntry(
	relative string,
	size int64,
	payload releaseFile,
	isPayload bool,
	wantMode fs.FileMode,
	source io.Reader,
	consume func(string, fs.FileMode, io.Reader) error,
) error {
	counted := &countingReader{reader: source}
	hash := sha256.New()
	stream := io.Reader(counted)
	if isPayload {
		stream = io.TeeReader(stream, hash)
	}
	if consume == nil {
		if _, err := io.Copy(io.Discard, stream); err != nil {
			return err
		}
	} else if err := consume(relative, wantMode, stream); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, stream); err != nil {
		return err
	}
	if counted.count != size {
		return fmt.Errorf("archive entry %s read %d bytes, want %d", relative, counted.count, size)
	}
	if isPayload && hex.EncodeToString(hash.Sum(nil)) != payload.SHA256 {
		return fmt.Errorf("archive payload %s digest is invalid", relative)
	}
	return nil
}

func (inspector archiveInspector) validateArchiveMembership(target releaseTarget, payloads []releaseFile, seen map[string]struct{}) error {
	if len(seen) != len(payloads)+len(target.Binaries) {
		return errors.New("archive membership count is invalid")
	}
	for _, payload := range payloads {
		if _, found := seen[payload.Name]; !found {
			return fmt.Errorf("archive is missing payload %s", payload.Name)
		}
	}
	for _, binary := range target.Binaries {
		if _, found := seen[binary]; !found {
			return fmt.Errorf("archive is missing binary %s", binary)
		}
	}
	return nil
}

func (inspector archiveInspector) archiveRoot(archive string) string {
	return strings.TrimSuffix(strings.TrimSuffix(archive, ".zip"), ".tar.gz")
}

func (inspector archiveInspector) findPayload(payloads []releaseFile, name string) (releaseFile, bool) {
	for _, payload := range payloads {
		if payload.Name == name {
			return payload, true
		}
	}
	return releaseFile{}, false
}

func (inspector archiveInspector) findTarget(targets []releaseTarget, goos, goarch string) (releaseTarget, bool) {
	for _, target := range targets {
		if target.GOOS == goos && target.GOARCH == goarch {
			return target, true
		}
	}
	return releaseTarget{}, false
}
