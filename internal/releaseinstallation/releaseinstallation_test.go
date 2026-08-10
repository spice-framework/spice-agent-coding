package releaseinstallation

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var fixtureExpectation = Expectation{
	Repository: "spice-agent-coding",
	Module:     "github.com/spice-framework/spice-agent-coding",
	Version:    "v0.1.0-preview.2",
}

const fixtureCommit = "0123456789abcdef0123456789abcdef01234567"

func TestVerifyCandidateBindsExactCanonicalMetadata(t *testing.T) {
	t.Parallel()
	candidate := t.TempDir()
	writeCandidateMetadata(t, candidate, candidateMetadata{
		Schema: 1, Profile: releaseProfile, Repository: fixtureExpectation.Repository,
		Module: fixtureExpectation.Module, Version: fixtureExpectation.Version,
	})
	set, err := VerifyCandidate(candidate, releaseFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if set.Version() != fixtureExpectation.Version {
		t.Fatalf("verified version = %q", set.Version())
	}

	staleCandidate := t.TempDir()
	writeCandidateMetadata(t, staleCandidate, candidateMetadata{
		Schema: 1, Profile: releaseProfile, Repository: fixtureExpectation.Repository,
		Module: fixtureExpectation.Module, Version: "v0.1.0-preview.3",
	})
	if _, err = VerifyCandidate(staleCandidate, releaseFixture(t)); err == nil {
		t.Fatal("preview 2 subjects satisfied a preview 3 candidate")
	}

	mismatchedCandidate := t.TempDir()
	writeCandidateMetadata(t, mismatchedCandidate, candidateMetadata{
		Schema: 1, Profile: releaseProfile, Repository: fixtureExpectation.Repository,
		Module: "example.com/wrong", Version: fixtureExpectation.Version,
	})
	if _, err = VerifyCandidate(mismatchedCandidate, releaseFixture(t)); err == nil {
		t.Fatal("subjects satisfied mismatched candidate module metadata")
	}
}

func TestCandidateExpectationRejectsInvalidOrNoncanonicalMetadata(t *testing.T) {
	t.Parallel()
	valid := candidateMetadata{
		Schema: 1, Profile: releaseProfile, Repository: fixtureExpectation.Repository,
		Module: fixtureExpectation.Module, Version: "v0.1.0-preview.3",
	}
	for _, test := range []struct {
		name    string
		mutate  func(*candidateMetadata)
		content func([]byte) []byte
	}{
		{name: "wrong schema", mutate: func(value *candidateMetadata) { value.Schema = 2 }},
		{name: "wrong profile", mutate: func(value *candidateMetadata) { value.Profile = "other" }},
		{name: "missing repository", mutate: func(value *candidateMetadata) { value.Repository = "" }},
		{name: "invalid version", mutate: func(value *candidateMetadata) { value.Version = "preview.3" }},
		{name: "noncanonical", content: func(content []byte) []byte {
			return bytes.ReplaceAll(content, []byte("  "), nil)
		}},
		{name: "trailing JSON", content: func(content []byte) []byte {
			return append(content, []byte("{}\n")...)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := t.TempDir()
			metadata := valid
			if test.mutate != nil {
				test.mutate(&metadata)
			}
			content, err := json.MarshalIndent(metadata, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			content = append(content, '\n')
			if test.content != nil {
				content = test.content(content)
			}
			writeTestFile(t, filepath.Join(candidate, "spice-release.json"), content)
			if _, err = candidateExpectation(candidate); err == nil {
				t.Fatal("candidateExpectation() error = nil")
			}
		})
	}
	if _, err := candidateExpectation("relative"); err == nil {
		t.Fatal("relative candidate root was accepted")
	}
}

func writeCandidateMetadata(t *testing.T, root string, metadata candidateMetadata) {
	t.Helper()
	content, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "spice-release.json"), append(content, '\n'))
}

func TestVerifyAndExtractValidatedSubjects(t *testing.T) {
	t.Parallel()
	directory := releaseFixture(t)
	set, err := Verify(directory, fixtureExpectation)
	if err != nil {
		t.Fatal(err)
	}
	if set.Version() != fixtureExpectation.Version || set.Commit() != fixtureCommit {
		t.Fatalf("verified identity = %q %q", set.Version(), set.Commit())
	}
	for _, target := range []targetExpectation{{goos: "linux", goarch: "amd64"}, {goos: "windows", goarch: "amd64"}} {
		destination := filepath.Join(t.TempDir(), "Unicode π extracted "+target.goos)
		root, extractErr := set.ExtractNative(destination, target.goos, target.goarch)
		if extractErr != nil {
			t.Fatalf("extract %s: %v", target.goos, extractErr)
		}
		binary := "spice-agent"
		if target.goos == "windows" {
			binary += ".exe"
		}
		content, readErr := os.ReadFile(filepath.Join(root, binary)) // #nosec G304 -- exact extracted fixture member.
		if readErr != nil || string(content) != "binary:"+binary {
			t.Fatalf("extracted %s = %q, %v", binary, content, readErr)
		}
	}
	if _, err = set.ExtractNative("relative", "linux", "amd64"); err == nil {
		t.Fatal("relative extraction directory was accepted")
	}
	existing := t.TempDir()
	if _, err = set.ExtractNative(existing, "linux", "amd64"); err == nil {
		t.Fatal("existing extraction directory was accepted")
	}
	if _, err = set.ExtractNative(filepath.Join(t.TempDir(), "new"), "plan9", "amd64"); err == nil {
		t.Fatal("unsupported native target was accepted")
	}
	archive := set.archives["linux/amd64"]
	changedArchive := []byte("changed after validation")
	writeTestFile(t, archive, changedArchive)
	set.digests[filepath.Base(archive)] = digestBytes(changedArchive)
	failedDestination := filepath.Join(t.TempDir(), "failed extraction")
	if _, err = set.ExtractNative(failedDestination, "linux", "amd64"); err == nil {
		t.Fatal("archive changed after validation was extracted")
	}
	if _, statErr := os.Stat(failedDestination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed extraction destination remained: %v", statErr)
	}
	var unavailable *Set
	if unavailable.Version() != "" || unavailable.Commit() != "" {
		t.Fatal("nil release set exposed identity")
	}
	if _, err = unavailable.ExtractNative(filepath.Join(t.TempDir(), "new"), "linux", "amd64"); err == nil {
		t.Fatal("nil release set extracted an archive")
	}
}

func TestVerifyRejectsSubjectAndChecksumDrift(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "missing subject", mutate: func(t *testing.T, directory string) {
			t.Helper()
			if err := os.Remove(filepath.Join(directory, "checksums.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unexpected subject", mutate: func(t *testing.T, directory string) {
			t.Helper()
			writeTestFile(t, filepath.Join(directory, "unexpected"), []byte("unexpected"))
		}},
		{name: "subject hash", mutate: func(t *testing.T, directory string) {
			t.Helper()
			writeTestFile(t, filepath.Join(directory, sbomName(fixtureExpectation)), []byte("{}\n"))
		}},
		{name: "partial checksums", mutate: func(t *testing.T, directory string) {
			t.Helper()
			path := filepath.Join(directory, "checksums.txt")
			content, err := os.ReadFile(path) // #nosec G304 -- exact fixture path.
			if err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, path, content[:bytes.IndexByte(content, '\n')+1])
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := releaseFixture(t)
			test.mutate(t, directory)
			if _, err := Verify(directory, fixtureExpectation); err == nil {
				t.Fatal("Verify() error = nil")
			}
		})
	}
	if _, err := Verify("relative", fixtureExpectation); err == nil {
		t.Fatal("relative subject directory was accepted")
	}
	if _, err := Verify(t.TempDir(), Expectation{}); err == nil {
		t.Fatal("invalid expectation was accepted")
	}
}

func TestReleaseMetadataAndSBOMValidationRejectsDrift(t *testing.T) {
	t.Parallel()
	metadata := fixtureMetadata(t, t.TempDir())
	valid := metadata
	if err := validateReleaseMetadata(valid, fixtureExpectation); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*releaseMetadata){
		func(value *releaseMetadata) { value.Schema = 2 },
		func(value *releaseMetadata) { value.Commit = "not-a-commit" },
		func(value *releaseMetadata) { value.Build.CGOEnabled = true },
		func(value *releaseMetadata) { value.Build.Identity.CommitValue = strings.Repeat("0", 40) },
		func(value *releaseMetadata) { value.Targets = value.Targets[:5] },
		func(value *releaseMetadata) { value.Targets[0].Archive = "wrong.tar.gz" },
		func(value *releaseMetadata) { value.Payloads = value.Payloads[:6] },
		func(value *releaseMetadata) { value.Payloads[0].SHA256 = "bad" },
	}
	for index, mutate := range mutations {
		candidate := valid
		candidate.Targets = slices.Clone(valid.Targets)
		candidate.Payloads = slices.Clone(valid.Payloads)
		mutate(&candidate)
		if err := validateReleaseMetadata(candidate, fixtureExpectation); err == nil {
			t.Fatalf("metadata mutation %d was accepted", index)
		}
	}
	sbom := filepath.Join(t.TempDir(), "sbom.json")
	writeTestFile(t, sbom, []byte(`{"spdxVersion":"SPDX-2.2"}`))
	if err := validateSBOM(sbom, fixtureExpectation); err == nil {
		t.Fatal("invalid SBOM identity was accepted")
	}
}

func TestArchiveValidationRejectsPathModePayloadAndMembershipDrift(t *testing.T) {
	t.Parallel()
	payload := releaseFile{Name: "LICENSE", SHA256: digestText("license"), Size: int64(len("license"))}
	target := releaseTarget{
		GOOS: "linux", GOARCH: "amd64", Archive: "fixture_linux_amd64.tar.gz",
		Binaries: []string{"spice-agent", "spice-agentd"},
	}
	for _, test := range []struct {
		name    string
		entries []archiveFixtureEntry
	}{
		{name: "valid", entries: []archiveFixtureEntry{
			{name: "LICENSE", mode: 0o644, content: "license"},
			{name: "spice-agent", mode: 0o755, content: "agent"},
			{name: "spice-agentd", mode: 0o755, content: "daemon"},
		}},
		{name: "traversal", entries: []archiveFixtureEntry{{name: "../escape", mode: 0o644, content: "license"}}},
		{name: "wrong mode", entries: []archiveFixtureEntry{
			{name: "LICENSE", mode: 0o755, content: "license"},
			{name: "spice-agent", mode: 0o755, content: "agent"},
			{name: "spice-agentd", mode: 0o755, content: "daemon"},
		}},
		{name: "payload digest", entries: []archiveFixtureEntry{
			{name: "LICENSE", mode: 0o644, content: "changed"},
			{name: "spice-agent", mode: 0o755, content: "agent"},
			{name: "spice-agentd", mode: 0o755, content: "daemon"},
		}},
		{name: "missing binary", entries: []archiveFixtureEntry{{name: "LICENSE", mode: 0o644, content: "license"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			file := filepath.Join(t.TempDir(), target.Archive)
			writeTarArchive(t, file, archiveRoot(target.Archive), test.entries)
			err := inspectArchive(file, target, []releaseFile{payload}, nil)
			if test.name == "valid" && err != nil {
				t.Fatal(err)
			}
			if test.name != "valid" && err == nil {
				t.Fatal("inspectArchive() error = nil")
			}
		})
	}
}

type archiveFixtureEntry struct {
	name, content string
	mode          os.FileMode
}

func releaseFixture(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	metadata := fixtureMetadata(t, directory)
	metadataContent, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	metadataContent = append(metadataContent, '\n')
	writeTestFile(t, filepath.Join(directory, releaseMetadataName(fixtureExpectation)), metadataContent)
	writeChecksums(t, directory, expectedSubjectNames(fixtureExpectation)[1:])
	return directory
}

func fixtureMetadata(t *testing.T, directory string) releaseMetadata {
	t.Helper()
	payloadContents := map[string]string{
		"LICENSE": "license", "README.md": "readme", "THIRD_PARTY_NOTICES.md": "notices",
		"docs/configuration.md": "configuration", "docs/installation.md": "installation",
		"docs/security.md": "security", "protocol-descriptors.pb": "descriptors",
	}
	payloads := make([]releaseFile, 0, len(expectedPayloadNames))
	for _, name := range expectedPayloadNames {
		content := payloadContents[name]
		payloads = append(payloads, releaseFile{Name: name, SHA256: digestText(content), Size: int64(len(content))})
	}
	targets := make([]releaseTarget, 0, len(supportedTargets))
	artifacts := make([]releaseFile, 0, 7)
	for _, current := range supportedTargets {
		extension := ".tar.gz"
		binaries := []string{"spice-agent", "spice-agentd"}
		if current.goos == "windows" {
			extension = ".zip"
			binaries = []string{"spice-agent.exe", "spice-agentd.exe"}
		}
		archive := artifactBase(fixtureExpectation) + "_" + current.goos + "_" + current.goarch + extension
		target := releaseTarget{GOOS: current.goos, GOARCH: current.goarch, Archive: archive, Binaries: binaries}
		targets = append(targets, target)
		entries := make([]archiveFixtureEntry, 0, len(payloads)+len(binaries))
		for _, payload := range payloads {
			entries = append(entries, archiveFixtureEntry{name: payload.Name, mode: 0o644, content: payloadContents[payload.Name]})
		}
		for _, binary := range binaries {
			entries = append(entries, archiveFixtureEntry{name: binary, mode: 0o755, content: "binary:" + binary})
		}
		path := filepath.Join(directory, archive)
		if current.goos == "windows" {
			writeZipArchive(t, path, archiveRoot(archive), entries)
		} else {
			writeTarArchive(t, path, archiveRoot(archive), entries)
		}
		artifacts = append(artifacts, fileMetadata(t, path))
	}
	sbom := sbomName(fixtureExpectation)
	sbomContent := []byte(`{"spdxVersion":"SPDX-2.3","dataLicense":"CC0-1.0","SPDXID":"SPDXRef-DOCUMENT","name":"spice-agent-coding v0.1.0-preview.2","documentNamespace":"https://github.com/spice-framework/spice-agent-coding/releases/v0.1.0-preview.2/spdx/test","packages":[{"name":"github.com/spice-framework/spice-agent-coding","versionInfo":"v0.1.0-preview.2"}]}` + "\n")
	writeTestFile(t, filepath.Join(directory, sbom), sbomContent)
	artifacts = append(artifacts, fileMetadata(t, filepath.Join(directory, sbom)))
	slices.SortFunc(artifacts, func(left, right releaseFile) int { return strings.Compare(left.Name, right.Name) })
	return releaseMetadata{
		Schema: 1, Profile: releaseProfile, Repository: fixtureExpectation.Repository,
		Module: fixtureExpectation.Module, Source: "https://github.com/spice-framework/spice-agent-coding",
		Version: fixtureExpectation.Version, Commit: fixtureCommit, SourceDateEpoch: 1,
		Go: "1.26.5", Toolchain: "go1.26.5",
		Build: releaseBuild{
			ModuleMode: "vendor", Trimpath: true, Environment: "closed", CacheIsolation: true,
			Source: "materialized-tagged-commit", GOAMD64: "v1", GOARM64: "v8.0",
			Identity: releaseIdentity{
				VersionSymbol: fixtureExpectation.Module + "/internal/distribution.Version",
				VersionValue:  "0.1.0-preview.2",
				CommitSymbol:  fixtureExpectation.Module + "/internal/distribution.Commit",
				CommitValue:   fixtureCommit,
			},
		},
		Targets: targets, Payloads: payloads, Artifacts: artifacts,
	}
}

func writeTarArchive(t *testing.T, file, root string, entries []archiveFixtureEntry) {
	t.Helper()
	opened, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(opened)
	archive := tar.NewWriter(compressed)
	for _, entry := range entries {
		content := []byte(entry.content)
		header := &tar.Header{Name: root + "/" + entry.name, Mode: int64(entry.mode), Size: int64(len(content))}
		if err = archive.WriteHeader(header); err == nil {
			_, err = archive.Write(content)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = errors.Join(archive.Close(), compressed.Close(), opened.Close()); err != nil {
		t.Fatal(err)
	}
}

func writeZipArchive(t *testing.T, file, root string, entries []archiveFixtureEntry) {
	t.Helper()
	opened, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(opened)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: root + "/" + entry.name, Method: zip.Deflate}
		header.SetMode(entry.mode)
		writer, createErr := archive.CreateHeader(header)
		if createErr == nil {
			_, createErr = io.WriteString(writer, entry.content)
		}
		if createErr != nil {
			t.Fatal(createErr)
		}
	}
	if err = errors.Join(archive.Close(), opened.Close()); err != nil {
		t.Fatal(err)
	}
}

func fileMetadata(t *testing.T, file string) releaseFile {
	t.Helper()
	content, err := os.ReadFile(file) // #nosec G304 -- exact fixture file.
	if err != nil {
		t.Fatal(err)
	}
	return releaseFile{Name: filepath.Base(file), SHA256: digestBytes(content), Size: int64(len(content))}
}

func writeChecksums(t *testing.T, directory string, names []string) {
	t.Helper()
	var content strings.Builder
	for _, name := range names {
		file, err := os.ReadFile(filepath.Join(directory, name)) // #nosec G304 -- exact fixture member.
		if err != nil {
			t.Fatal(err)
		}
		content.WriteString(digestBytes(file) + "  " + name + "\n")
	}
	writeTestFile(t, filepath.Join(directory, "checksums.txt"), []byte(content.String()))
}

func writeTestFile(t *testing.T, file string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func digestText(value string) string { return digestBytes([]byte(value)) }

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
