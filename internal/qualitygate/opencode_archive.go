package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

type opencodeArchive struct {
	maximumEntryBytes int64
}

func newOpenCodeArchive() opencodeArchive {
	return opencodeArchive{maximumEntryBytes: 256 << 20}
}

func (archive opencodeArchive) ValidateRoot(path string, specification opencodePackage) error {
	want := []string{"package/LICENSE", "package/bin/opencode.exe", "package/package.json", "package/postinstall.mjs"}
	entries, metadata, err := archive.inspect(path, "")
	if err != nil {
		return err
	}
	slices.Sort(entries)
	if !slices.Equal(entries, want) || metadata.Name != specification.Name || metadata.Version != openCodeVersion {
		return errors.New("OpenCode root package contents differ from the reviewed package")
	}
	return nil
}

func (archive opencodeArchive) ExtractExecutable(path, destination string, specification opencodePackage) error {
	want := []string{specification.ExecutableEntry, "package/package.json"}
	entries, metadata, err := archive.inspect(path, destination)
	if err != nil {
		return err
	}
	slices.Sort(entries)
	slices.Sort(want)
	if !slices.Equal(entries, want) || metadata.Name != specification.Name || metadata.Version != openCodeVersion {
		return errors.New("OpenCode platform package contents differ from the reviewed package")
	}
	return nil
}

func (archive opencodeArchive) inspect(path, executableDestination string) (
	entries []string,
	metadata opencodePackageMetadata,
	inspectErr error,
) {
	file, err := os.Open(path) // #nosec G304 -- path is owned by the evaluation workspace.
	if err != nil {
		return nil, opencodePackageMetadata{}, fmt.Errorf("open OpenCode package: %w", err)
	}
	defer func() {
		inspectErr = errors.Join(inspectErr, file.Close())
	}()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, opencodePackageMetadata{}, fmt.Errorf("open OpenCode gzip stream: %w", err)
	}
	defer func() {
		inspectErr = errors.Join(inspectErr, compressed.Close())
	}()
	reader := tar.NewReader(compressed)
	entries = make([]string, 0, 4)
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nil, opencodePackageMetadata{}, fmt.Errorf("read OpenCode package: %w", nextErr)
		}
		if err = archive.validateHeader(header, entries); err != nil {
			return nil, opencodePackageMetadata{}, err
		}
		entries = append(entries, header.Name)
		if err = archive.readEntry(reader, header, executableDestination, &metadata); err != nil {
			return nil, opencodePackageMetadata{}, err
		}
	}
	return entries, metadata, nil
}

func (archive opencodeArchive) validateHeader(header *tar.Header, entries []string) error {
	if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > archive.maximumEntryBytes ||
		slices.Contains(entries, header.Name) {
		return errors.New("OpenCode package contains an unsafe entry")
	}
	return nil
}

func (archive opencodeArchive) readEntry(
	reader io.Reader,
	header *tar.Header,
	executableDestination string,
	metadata *opencodePackageMetadata,
) error {
	if header.Name == "package/package.json" {
		if header.Size > 4096 {
			return errors.New("OpenCode package metadata is oversized")
		}
		decoder := json.NewDecoder(io.LimitReader(reader, header.Size))
		if err := decoder.Decode(metadata); err != nil {
			return fmt.Errorf("decode OpenCode package metadata: %w", err)
		}
		return nil
	}
	if executableDestination == "" || !stringsEqualArchivePath(header.Name, filepath.Base(executableDestination)) {
		return nil
	}
	return archive.writeExecutable(reader, header.Size, executableDestination)
}

func (archive opencodeArchive) writeExecutable(reader io.Reader, size int64, destination string) error {
	if size == 0 {
		return errors.New("OpenCode executable is empty")
	}
	// #nosec G302,G304 -- destination is workspace-owned and must be executable by its owner on Unix.
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return fmt.Errorf("create OpenCode executable: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(reader, size))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written != size {
		return errors.Join(errors.New("write complete OpenCode executable"), copyErr, closeErr)
	}
	return nil
}

func stringsEqualArchivePath(name, executableBase string) bool {
	return name == "package/bin/"+executableBase
}
