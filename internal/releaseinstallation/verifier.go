package releaseinstallation

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	releaseProfile           = "go-distribution-v1"
	maximumCandidateMetadata = 4 << 10
	maximumEntry             = 128 << 20
)

// Verifier owns offline release-subject validation.
type Verifier struct{}

// NewVerifier returns stateless offline release-subject validation.
func NewVerifier() Verifier {
	return Verifier{}
}

func (Verifier) supportedTargets() []targetExpectation {
	return []targetExpectation{
		{goos: "linux", goarch: "amd64"},
		{goos: "linux", goarch: "arm64"},
		{goos: "darwin", goarch: "amd64"},
		{goos: "darwin", goarch: "arm64"},
		{goos: "windows", goarch: "amd64"},
		{goos: "windows", goarch: "arm64"},
	}
}

func (Verifier) expectedPayloadNames() []string {
	return []string{
		"LICENSE",
		"README.md",
		"THIRD_PARTY_NOTICES.md",
		"docs/configuration.md",
		"docs/installation.md",
		"docs/security.md",
		"protocol-descriptors.pb",
	}
}

// Verify validates exact subject membership, checksums, release metadata,
// SPDX identity, and all six archive structures without network access.
func (verifier Verifier) Verify(directory string, expectation Expectation) (*Set, error) {
	if err := verifier.validateExpectation(expectation); err != nil {
		return nil, err
	}
	if err := verifier.validateDirectory(directory); err != nil {
		return nil, err
	}
	names := verifier.expectedSubjectNames(expectation)
	if err := verifier.validateSubjectMembership(directory, names); err != nil {
		return nil, err
	}
	checksums, err := verifier.readChecksums(filepath.Join(directory, "checksums.txt"), names[1:])
	if err != nil {
		return nil, err
	}
	for _, name := range names[1:] {
		if err = verifier.verifyFile(filepath.Join(directory, name), checksums[name]); err != nil {
			return nil, fmt.Errorf("verify subject %s: %w", name, err)
		}
	}
	metadataName := verifier.releaseMetadataName(expectation)
	metadata, err := verifier.readReleaseMetadata(filepath.Join(directory, metadataName), expectation)
	if err != nil {
		return nil, err
	}
	if err = verifier.validateMetadataChecksums(directory, metadata, checksums, expectation); err != nil {
		return nil, err
	}
	if err = verifier.validateSBOM(filepath.Join(directory, verifier.sbomName(expectation)), expectation); err != nil {
		return nil, err
	}
	set := &Set{
		metadata: metadata,
		archives: make(map[string]string, 6), digests: checksums,
	}
	for _, target := range metadata.Targets {
		archivePath := filepath.Join(directory, target.Archive)
		if err = (archiveInspector{}).inspectArchive(archivePath, target, metadata.Payloads, nil); err != nil {
			return nil, fmt.Errorf("validate archive %s: %w", target.Archive, err)
		}
		set.archives[target.GOOS+"/"+target.GOARCH] = archivePath
	}
	return set, nil
}

// VerifyCandidate binds installed-byte verification to the exact inert
// release identity committed by the candidate checkout. It does not accept a
// caller-supplied version and performs no network or Git operations.
func (verifier Verifier) VerifyCandidate(candidateRoot, directory string) (*Set, error) {
	expectation, err := verifier.candidateExpectation(candidateRoot)
	if err != nil {
		return nil, err
	}
	return verifier.Verify(directory, expectation)
}

func (verifier Verifier) candidateExpectation(root string) (Expectation, error) {
	if err := verifier.validatePhysicalDirectory(root, "release candidate root"); err != nil {
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
	if err = verifier.validateExpectation(expectation); err != nil {
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

func (verifier Verifier) validateExpectation(expectation Expectation) error {
	if expectation.Repository == "" || expectation.Module == "" ||
		!strings.HasPrefix(expectation.Version, "v") || strings.TrimSpace(expectation.Version) != expectation.Version {
		return errors.New("release expectation is invalid")
	}
	return nil
}

func (verifier Verifier) validateDirectory(directory string) error {
	return verifier.validatePhysicalDirectory(directory, "verified artifact directory")
}

func (verifier Verifier) validatePhysicalDirectory(directory, description string) error {
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

func (verifier Verifier) expectedSubjectNames(expectation Expectation) []string {
	base := verifier.artifactBase(expectation)
	names := []string{"checksums.txt"}
	for _, target := range verifier.supportedTargets() {
		extension := ".tar.gz"
		if target.goos == "windows" {
			extension = ".zip"
		}
		names = append(names, base+"_"+target.goos+"_"+target.goarch+extension)
	}
	names = append(names, verifier.releaseMetadataName(expectation), verifier.sbomName(expectation))
	slices.Sort(names[1:])
	return names
}

func (verifier Verifier) artifactBase(expectation Expectation) string {
	return expectation.Repository + "_" + strings.TrimPrefix(expectation.Version, "v")
}

func (verifier Verifier) releaseMetadataName(expectation Expectation) string {
	return verifier.artifactBase(expectation) + "_release.json"
}

func (verifier Verifier) sbomName(expectation Expectation) string {
	return verifier.artifactBase(expectation) + "_sbom.spdx.json"
}

func (verifier Verifier) validateSubjectMembership(directory string, expected []string) error {
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

func (verifier Verifier) readChecksums(file string, expected []string) (map[string]string, error) {
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

func (verifier Verifier) verifyFile(file, expected string) error {
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

func (verifier Verifier) readReleaseMetadata(file string, expectation Expectation) (releaseMetadata, error) {
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
	if err = verifier.validateReleaseMetadata(metadata, expectation); err != nil {
		return releaseMetadata{}, err
	}
	return metadata, nil
}

func (verifier Verifier) validateReleaseMetadata(metadata releaseMetadata, expectation Expectation) error {
	if err := verifier.validateMetadataIdentity(metadata, expectation); err != nil {
		return err
	}
	if err := verifier.validateBuildMetadata(metadata.Build, metadata.Commit, expectation); err != nil {
		return err
	}
	if err := verifier.validateTargets(metadata.Targets, expectation); err != nil {
		return err
	}
	return verifier.validatePayloads(metadata.Payloads)
}

func (verifier Verifier) validateMetadataIdentity(metadata releaseMetadata, expectation Expectation) error {
	if metadata.Schema != 1 || metadata.Profile != releaseProfile ||
		metadata.Repository != expectation.Repository || metadata.Module != expectation.Module ||
		metadata.Source != "https://github.com/spice-framework/"+expectation.Repository ||
		metadata.Version != expectation.Version || !verifier.validCommit(metadata.Commit) ||
		metadata.SourceDateEpoch <= 0 || metadata.Go != "1.26.5" || metadata.Toolchain != "go1.26.5" {
		return errors.New("release metadata identity is invalid")
	}
	return nil
}

func (verifier Verifier) validateBuildMetadata(build releaseBuild, commit string, expectation Expectation) error {
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

func (verifier Verifier) validCommit(value string) bool {
	if len(value) != 40 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 20
}

func (verifier Verifier) validateTargets(targets []releaseTarget, expectation Expectation) error {
	if len(targets) != len(verifier.supportedTargets()) {
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
		wantArchive := verifier.artifactBase(expectation) + "_" + target.GOOS + "_" + target.GOARCH + extension
		if target.Archive != wantArchive || !slices.Equal(target.Binaries, binaries) {
			return fmt.Errorf("release target %s is invalid", key)
		}
	}
	for _, target := range verifier.supportedTargets() {
		if _, found := seen[target.goos+"/"+target.goarch]; !found {
			return errors.New("release metadata is missing a supported target")
		}
	}
	return nil
}

func (verifier Verifier) validatePayloads(payloads []releaseFile) error {
	if len(payloads) != len(verifier.expectedPayloadNames()) {
		return errors.New("release metadata payload count is invalid")
	}
	for index, payload := range payloads {
		if payload.Name != verifier.expectedPayloadNames()[index] || payload.Size <= 0 || !verifier.validSHA256(payload.SHA256) {
			return fmt.Errorf("release payload %d is invalid", index+1)
		}
	}
	return nil
}

func (verifier Verifier) validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func (verifier Verifier) validateMetadataChecksums(
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
	expected := verifier.expectedSubjectNames(expectation)[1:]
	expected = slices.DeleteFunc(slices.Clone(expected), func(name string) bool {
		return name == verifier.releaseMetadataName(expectation)
	})
	for _, name := range expected {
		if name == verifier.sbomName(expectation) || strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".tar.gz") {
			if _, found := slices.BinarySearchFunc(metadata.Artifacts, name, func(file releaseFile, value string) int {
				return strings.Compare(file.Name, value)
			}); !found {
				return fmt.Errorf("release artifact metadata is missing %s", name)
			}
		}
	}
	return nil
}

func (verifier Verifier) validateSBOM(file string, expectation Expectation) error {
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
