// Package releaseinstallation validates and extracts independently verified
// Spice Agent distribution subjects for installed-byte acceptance.
package releaseinstallation

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

const (
	releaseProfile           = "go-distribution-v1"
	maximumCandidateMetadata = 4 << 10
	maximumEntry             = 128 << 20
)

var supportedTargets = []targetExpectation{
	{goos: "linux", goarch: "amd64"},
	{goos: "linux", goarch: "arm64"},
	{goos: "darwin", goarch: "amd64"},
	{goos: "darwin", goarch: "arm64"},
	{goos: "windows", goarch: "amd64"},
	{goos: "windows", goarch: "arm64"},
}

var expectedPayloadNames = []string{
	"LICENSE",
	"README.md",
	"THIRD_PARTY_NOTICES.md",
	"docs/configuration.md",
	"docs/installation.md",
	"docs/security.md",
	"protocol-descriptors.pb",
}

// Expectation identifies the immutable release whose independently verified
// subjects are allowed into the installed-byte gate.
type Expectation struct {
	Repository string
	Module     string
	Version    string
}

type candidateMetadata struct {
	Schema     int    `json:"schema"`
	Profile    string `json:"profile"`
	Repository string `json:"repository"`
	Module     string `json:"module"`
	Version    string `json:"version"`
}

// Set is a completely validated nine-subject distribution set.
type Set struct {
	metadata releaseMetadata
	archives map[string]string
	digests  map[string]string
}

// Verify validates exact subject membership, checksums, release metadata,
// SPDX identity, and all six archive structures without network access.
func Verify(directory string, expectation Expectation) (*Set, error) {
	if err := validateExpectation(expectation); err != nil {
		return nil, err
	}
	if err := validateDirectory(directory); err != nil {
		return nil, err
	}
	names := expectedSubjectNames(expectation)
	if err := validateSubjectMembership(directory, names); err != nil {
		return nil, err
	}
	checksums, err := readChecksums(filepath.Join(directory, "checksums.txt"), names[1:])
	if err != nil {
		return nil, err
	}
	for _, name := range names[1:] {
		if err = verifyFile(filepath.Join(directory, name), checksums[name]); err != nil {
			return nil, fmt.Errorf("verify subject %s: %w", name, err)
		}
	}
	metadataName := releaseMetadataName(expectation)
	metadata, err := readReleaseMetadata(filepath.Join(directory, metadataName), expectation)
	if err != nil {
		return nil, err
	}
	if err = validateMetadataChecksums(directory, metadata, checksums, expectation); err != nil {
		return nil, err
	}
	if err = validateSBOM(filepath.Join(directory, sbomName(expectation)), expectation); err != nil {
		return nil, err
	}
	set := &Set{
		metadata: metadata,
		archives: make(map[string]string, 6), digests: checksums,
	}
	for _, target := range metadata.Targets {
		archivePath := filepath.Join(directory, target.Archive)
		if err = inspectArchive(archivePath, target, metadata.Payloads, nil); err != nil {
			return nil, fmt.Errorf("validate archive %s: %w", target.Archive, err)
		}
		set.archives[target.GOOS+"/"+target.GOARCH] = archivePath
	}
	return set, nil
}

// VerifyCandidate binds installed-byte verification to the exact inert
// release identity committed by the candidate checkout. It does not accept a
// caller-supplied version and performs no network or Git operations.
func VerifyCandidate(candidateRoot, directory string) (*Set, error) {
	expectation, err := candidateExpectation(candidateRoot)
	if err != nil {
		return nil, err
	}
	return Verify(directory, expectation)
}

func candidateExpectation(root string) (Expectation, error) {
	if err := validatePhysicalDirectory(root, "release candidate root"); err != nil {
		return Expectation{}, err
	}
	file := filepath.Join(root, "spice-release.json")
	info, err := os.Lstat(file)
	if err != nil {
		return Expectation{}, fmt.Errorf("inspect candidate release metadata: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maximumCandidateMetadata {
		return Expectation{}, errors.New("candidate release metadata must be a bounded physical regular file")
	}
	content, err := os.ReadFile(file) // #nosec G304 -- fixed file beneath a validated physical candidate root.
	if err != nil {
		return Expectation{}, fmt.Errorf("read candidate release metadata: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var metadata candidateMetadata
	if err = decoder.Decode(&metadata); err != nil {
		return Expectation{}, fmt.Errorf("decode candidate release metadata: %w", err)
	}
	var trailing json.RawMessage
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Expectation{}, errors.New("candidate release metadata has trailing JSON values")
	}
	expectation := Expectation{
		Repository: metadata.Repository,
		Module:     metadata.Module,
		Version:    metadata.Version,
	}
	if metadata.Schema != 1 || metadata.Profile != releaseProfile {
		return Expectation{}, fmt.Errorf(
			"candidate release metadata must identify schema 1 profile %q", releaseProfile,
		)
	}
	if err = validateExpectation(expectation); err != nil {
		return Expectation{}, err
	}
	canonical, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return Expectation{}, fmt.Errorf("encode candidate release metadata: %w", err)
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(content, canonical) {
		return Expectation{}, errors.New("candidate release metadata is not in canonical deterministic form")
	}
	return expectation, nil
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
	installRoot := filepath.Join(destination, archiveRoot(target.Archive))
	err = inspectArchive(archivePath, target, set.metadata.Payloads, func(
		relative string, mode fs.FileMode, reader io.Reader,
	) error {
		return extractFile(installRoot, relative, mode, reader)
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
	target, found := findTarget(set.metadata.Targets, goos, goarch)
	if !found {
		return "", releaseTarget{}, errors.New("release target metadata is unavailable")
	}
	if err := verifyFile(archivePath, set.digests[target.Archive]); err != nil {
		return "", releaseTarget{}, fmt.Errorf("revalidate native release archive: %w", err)
	}
	return archivePath, target, nil
}

func extractFile(root, relative string, mode fs.FileMode, reader io.Reader) error {
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

type targetExpectation struct{ goos, goarch string }

type releaseMetadata struct {
	Schema          int             `json:"schema"`
	Profile         string          `json:"profile"`
	Repository      string          `json:"repository"`
	Module          string          `json:"module"`
	Source          string          `json:"source"`
	Version         string          `json:"version"`
	Commit          string          `json:"commit"`
	SourceDateEpoch int64           `json:"source_date_epoch"`
	Go              string          `json:"go"`
	Toolchain       string          `json:"toolchain"`
	Build           releaseBuild    `json:"build"`
	Targets         []releaseTarget `json:"targets"`
	Payloads        []releaseFile   `json:"payloads"`
	Artifacts       []releaseFile   `json:"artifacts"`
}

type releaseBuild struct {
	ModuleMode     string          `json:"module_mode"`
	CGOEnabled     bool            `json:"cgo_enabled"`
	Trimpath       bool            `json:"trimpath"`
	BuildVCS       bool            `json:"build_vcs"`
	BuildID        string          `json:"build_id"`
	Environment    string          `json:"environment"`
	CacheIsolation bool            `json:"cache_isolation"`
	Source         string          `json:"source"`
	GOAMD64        string          `json:"goamd64"`
	GOARM64        string          `json:"goarm64"`
	Identity       releaseIdentity `json:"identity"`
}

type releaseIdentity struct {
	VersionSymbol string `json:"version_symbol"`
	VersionValue  string `json:"version_value"`
	CommitSymbol  string `json:"commit_symbol"`
	CommitValue   string `json:"commit_value"`
}

type releaseTarget struct {
	GOOS     string   `json:"goos"`
	GOARCH   string   `json:"goarch"`
	Archive  string   `json:"archive"`
	Binaries []string `json:"binaries"`
}

type releaseFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func validateExpectation(expectation Expectation) error {
	if expectation.Repository == "" || expectation.Module == "" ||
		!strings.HasPrefix(expectation.Version, "v") || strings.TrimSpace(expectation.Version) != expectation.Version {
		return errors.New("release expectation is invalid")
	}
	return nil
}

func validateDirectory(directory string) error {
	return validatePhysicalDirectory(directory, "verified artifact directory")
}

func validatePhysicalDirectory(directory, description string) error {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return fmt.Errorf("%s must be canonical and absolute", description)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", description, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must be a physical directory", description)
	}
	return nil
}

func expectedSubjectNames(expectation Expectation) []string {
	base := artifactBase(expectation)
	names := []string{"checksums.txt"}
	for _, target := range supportedTargets {
		extension := ".tar.gz"
		if target.goos == "windows" {
			extension = ".zip"
		}
		names = append(names, base+"_"+target.goos+"_"+target.goarch+extension)
	}
	names = append(names, releaseMetadataName(expectation), sbomName(expectation))
	slices.Sort(names[1:])
	return names
}

func artifactBase(expectation Expectation) string {
	return expectation.Repository + "_" + strings.TrimPrefix(expectation.Version, "v")
}

func releaseMetadataName(expectation Expectation) string {
	return artifactBase(expectation) + "_release.json"
}

func sbomName(expectation Expectation) string { return artifactBase(expectation) + "_sbom.spdx.json" }

func validateSubjectMembership(directory string, expected []string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read verified artifact directory: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("release subject %s is not a physical regular file", entry.Name())
		}
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	want := slices.Clone(expected)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		return fmt.Errorf("verified artifact directory contains %v, want exact subjects %v", names, want)
	}
	return nil
}

func readChecksums(file string, expected []string) (map[string]string, error) {
	content, err := os.ReadFile(file) // #nosec G304 -- exact member of validated subject directory.
	if err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}
	if len(content) == 0 || content[len(content)-1] != '\n' || strings.Contains(string(content), "\r") {
		return nil, errors.New("checksums must be non-empty canonical LF text")
	}
	result := make(map[string]string, len(expected))
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(lines) != len(expected) {
		return nil, fmt.Errorf("checksums contains %d entries, want %d", len(lines), len(expected))
	}
	for index, line := range lines {
		digest, name, found := strings.Cut(line, "  ")
		if !found || name != expected[index] || len(digest) != sha256.Size*2 || digest != strings.ToLower(digest) {
			return nil, fmt.Errorf("checksums entry %d is not canonical", index+1)
		}
		decoded, decodeErr := hex.DecodeString(digest)
		if decodeErr != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("checksums entry %d has an invalid digest", index+1)
		}
		result[name] = digest
	}
	return result, nil
}

func verifyFile(file, expected string) error {
	opened, err := os.Open(file) // #nosec G304 -- exact member of validated subject directory.
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, opened)
	closeErr := opened.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return errors.New("SHA-256 does not match checksums.txt")
	}
	return nil
}

func readReleaseMetadata(file string, expectation Expectation) (releaseMetadata, error) {
	opened, err := os.Open(file) // #nosec G304 -- exact validated metadata subject.
	if err != nil {
		return releaseMetadata{}, err
	}
	defer opened.Close() //nolint:errcheck // Read-only close cannot change validation.
	decoder := json.NewDecoder(bufio.NewReader(opened))
	decoder.DisallowUnknownFields()
	var metadata releaseMetadata
	if err = decoder.Decode(&metadata); err != nil {
		return releaseMetadata{}, fmt.Errorf("decode release metadata: %w", err)
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return releaseMetadata{}, errors.New("release metadata contains trailing JSON")
	}
	if err = validateReleaseMetadata(metadata, expectation); err != nil {
		return releaseMetadata{}, err
	}
	return metadata, nil
}

func validateReleaseMetadata(metadata releaseMetadata, expectation Expectation) error {
	if err := validateMetadataIdentity(metadata, expectation); err != nil {
		return err
	}
	if err := validateBuildMetadata(metadata.Build, metadata.Commit, expectation); err != nil {
		return err
	}
	if err := validateTargets(metadata.Targets, expectation); err != nil {
		return err
	}
	return validatePayloads(metadata.Payloads)
}

func validateMetadataIdentity(metadata releaseMetadata, expectation Expectation) error {
	if metadata.Schema != 1 || metadata.Profile != releaseProfile ||
		metadata.Repository != expectation.Repository || metadata.Module != expectation.Module ||
		metadata.Source != "https://github.com/spice-framework/"+expectation.Repository ||
		metadata.Version != expectation.Version || !validCommit(metadata.Commit) ||
		metadata.SourceDateEpoch <= 0 || metadata.Go != "1.26.5" || metadata.Toolchain != "go1.26.5" {
		return errors.New("release metadata identity is invalid")
	}
	return nil
}

func validateBuildMetadata(build releaseBuild, commit string, expectation Expectation) error {
	versionValue := strings.TrimPrefix(expectation.Version, "v")
	identityPrefix := expectation.Module + "/internal/distribution."
	if build.ModuleMode != "vendor" || build.CGOEnabled || !build.Trimpath || build.BuildVCS ||
		build.BuildID != "" || build.Environment != "closed" || !build.CacheIsolation ||
		build.Source != "materialized-tagged-commit" || build.GOAMD64 != "v1" || build.GOARM64 != "v8.0" ||
		build.Identity.VersionSymbol != identityPrefix+"Version" || build.Identity.VersionValue != versionValue ||
		build.Identity.CommitSymbol != identityPrefix+"Commit" || build.Identity.CommitValue != commit {
		return errors.New("release build metadata is invalid")
	}
	return nil
}

func validCommit(value string) bool {
	if len(value) != 40 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 20
}

func validateTargets(targets []releaseTarget, expectation Expectation) error {
	if len(targets) != len(supportedTargets) {
		return errors.New("release metadata target count is invalid")
	}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		key := target.GOOS + "/" + target.GOARCH
		if _, duplicate := seen[key]; duplicate {
			return errors.New("release metadata contains a duplicate target")
		}
		seen[key] = struct{}{}
		extension := ".tar.gz"
		binaries := []string{"spice-agent", "spice-agentd"}
		if target.GOOS == "windows" {
			extension = ".zip"
			binaries = []string{"spice-agent.exe", "spice-agentd.exe"}
		}
		wantArchive := artifactBase(expectation) + "_" + target.GOOS + "_" + target.GOARCH + extension
		if target.Archive != wantArchive || !slices.Equal(target.Binaries, binaries) {
			return fmt.Errorf("release target %s is invalid", key)
		}
	}
	for _, target := range supportedTargets {
		if _, found := seen[target.goos+"/"+target.goarch]; !found {
			return errors.New("release metadata is missing a supported target")
		}
	}
	return nil
}

func validatePayloads(payloads []releaseFile) error {
	if len(payloads) != len(expectedPayloadNames) {
		return errors.New("release metadata payload count is invalid")
	}
	for index, payload := range payloads {
		if payload.Name != expectedPayloadNames[index] || payload.Size <= 0 || !validSHA256(payload.SHA256) {
			return fmt.Errorf("release payload %d is invalid", index+1)
		}
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validateMetadataChecksums(
	directory string,
	metadata releaseMetadata,
	checksums map[string]string,
	expectation Expectation,
) error {
	if len(metadata.Artifacts) != 7 {
		return errors.New("release metadata artifact count is invalid")
	}
	previous := ""
	for _, artifact := range metadata.Artifacts {
		if artifact.Name <= previous || artifact.Size <= 0 || artifact.SHA256 != checksums[artifact.Name] {
			return fmt.Errorf("release artifact metadata for %s is invalid", artifact.Name)
		}
		previous = artifact.Name
		info, err := os.Stat(filepath.Join(directory, artifact.Name))
		if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.Size {
			return fmt.Errorf("release artifact file metadata for %s is invalid", artifact.Name)
		}
	}
	expected := expectedSubjectNames(expectation)[1:]
	expected = slices.DeleteFunc(slices.Clone(expected), func(name string) bool {
		return name == releaseMetadataName(expectation)
	})
	for _, name := range expected {
		if name == sbomName(expectation) || strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".tar.gz") {
			if _, found := slices.BinarySearchFunc(metadata.Artifacts, name, func(file releaseFile, value string) int {
				return strings.Compare(file.Name, value)
			}); !found {
				return fmt.Errorf("release artifact metadata is missing %s", name)
			}
		}
	}
	return nil
}

func validateSBOM(file string, expectation Expectation) error {
	content, err := os.ReadFile(file) // #nosec G304 -- exact validated SBOM subject.
	if err != nil {
		return err
	}
	var document struct {
		SPDXVersion       string `json:"spdxVersion"`
		DataLicense       string `json:"dataLicense"`
		SPDXID            string `json:"SPDXID"`
		Name              string `json:"name"`
		DocumentNamespace string `json:"documentNamespace"`
		Packages          []struct {
			Name        string `json:"name"`
			VersionInfo string `json:"versionInfo"`
		} `json:"packages"`
	}
	if err = json.Unmarshal(content, &document); err != nil {
		return fmt.Errorf("decode SPDX SBOM: %w", err)
	}
	if document.SPDXVersion != "SPDX-2.3" || document.DataLicense != "CC0-1.0" ||
		document.SPDXID != "SPDXRef-DOCUMENT" ||
		document.Name != expectation.Repository+" "+expectation.Version ||
		!strings.HasPrefix(document.DocumentNamespace, "https://github.com/spice-framework/"+expectation.Repository+"/releases/"+expectation.Version+"/spdx/") ||
		len(document.Packages) == 0 || document.Packages[0].Name != expectation.Module ||
		document.Packages[0].VersionInfo != expectation.Version {
		return errors.New("SPDX SBOM identity is invalid")
	}
	return nil
}

func inspectArchive(
	file string,
	target releaseTarget,
	payloads []releaseFile,
	consume func(string, fs.FileMode, io.Reader) error,
) error {
	if strings.HasSuffix(file, ".zip") {
		return inspectZip(file, target, payloads, consume)
	}
	if strings.HasSuffix(file, ".tar.gz") {
		return inspectTar(file, target, payloads, consume)
	}
	return errors.New("release archive extension is unsupported")
}

func inspectZip(file string, target releaseTarget, payloads []releaseFile, consume func(string, fs.FileMode, io.Reader) error) error {
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
		entryErr := inspectEntry(entry.Name, entry.Mode(), int64(entry.UncompressedSize64), opened, target, payloads, seen, consume)
		closeErr := opened.Close()
		if entryErr != nil {
			return entryErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return validateArchiveMembership(target, payloads, seen)
}

func inspectTar(file string, target releaseTarget, payloads []releaseFile, consume func(string, fs.FileMode, io.Reader) error) error {
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
		if err = inspectEntry(header.Name, fs.FileMode(uint32(header.Mode)), header.Size, reader, target, payloads, seen, consume); err != nil {
			return err
		}
	}
	return validateArchiveMembership(target, payloads, seen)
}

func inspectEntry(
	name string,
	mode fs.FileMode,
	size int64,
	reader io.Reader,
	target releaseTarget,
	payloads []releaseFile,
	seen map[string]struct{},
	consume func(string, fs.FileMode, io.Reader) error,
) error {
	relative, payload, isPayload, wantMode, err := validateArchiveEntry(name, mode, size, target, payloads, seen)
	if err != nil {
		return err
	}
	return consumeArchiveEntry(relative, size, payload, isPayload, wantMode, reader, consume)
}

func validateArchiveEntry(
	name string,
	mode fs.FileMode,
	size int64,
	target releaseTarget,
	payloads []releaseFile,
	seen map[string]struct{},
) (string, releaseFile, bool, fs.FileMode, error) {
	relative, err := validateArchivePath(name, archiveRoot(target.Archive), seen)
	if err != nil {
		return "", releaseFile{}, false, 0, err
	}
	payload, isPayload := findPayload(payloads, relative)
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

func validateArchivePath(name, root string, seen map[string]struct{}) (string, error) {
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

func consumeArchiveEntry(
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

type countingReader struct {
	reader io.Reader
	count  int64
}

func (reader *countingReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.count += int64(count)
	return count, err
}

func validateArchiveMembership(target releaseTarget, payloads []releaseFile, seen map[string]struct{}) error {
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

func archiveRoot(archive string) string {
	return strings.TrimSuffix(strings.TrimSuffix(archive, ".zip"), ".tar.gz")
}

func findPayload(payloads []releaseFile, name string) (releaseFile, bool) {
	for _, payload := range payloads {
		if payload.Name == name {
			return payload, true
		}
	}
	return releaseFile{}, false
}

func findTarget(targets []releaseTarget, goos, goarch string) (releaseTarget, bool) {
	for _, target := range targets {
		if target.GOOS == goos && target.GOARCH == goarch {
			return target, true
		}
	}
	return releaseTarget{}, false
}
