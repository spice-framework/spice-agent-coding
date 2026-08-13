package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestReleaseMetadataIsStrictAndCanonical(t *testing.T) {
	t.Parallel()
	valid := "{\n" +
		"  \"schema\": 1,\n" +
		"  \"profile\": \"" + releaseProfile + "\",\n" +
		"  \"repository\": \"" + releaseRepository + "\",\n" +
		"  \"module\": \"" + modulePath + "\",\n" +
		"  \"version\": \"" + releaseVersion + "\"\n" +
		"}\n"
	root := t.TempDir()
	writeFile(t, root, "spice-release.json", valid)
	if err := (releaseMetadata{}).checkReleaseMetadata(root); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "unknown field", content: strings.Replace(valid, "\n}\n", ",\n  \"extra\": true\n}\n", 1)},
		{name: "wrong profile", content: strings.Replace(valid, releaseProfile, "starter-v1", 1)},
		{name: "wrong repository", content: strings.Replace(valid, releaseRepository, "another", 1)},
		{name: "wrong module", content: strings.Replace(valid, modulePath, "example.com/other", 1)},
		{name: "wrong version", content: strings.Replace(valid, releaseVersion, "v0.2.0", 1)},
		{name: "immutable failed preview", content: strings.Replace(valid, releaseVersion, "v0.1.0-preview.1", 1)},
		{name: "stale published preview", content: strings.Replace(valid, releaseVersion, "v0.1.0-preview.2", 1)},
		{name: "stale failed preview", content: strings.Replace(valid, releaseVersion, "v0.1.0-preview.3", 1)},
		{name: "stale published preview 4", content: strings.Replace(valid, releaseVersion, "v0.1.0-preview.4", 1)},
		{name: "trailing value", content: valid + "{}\n"},
		{name: "noncanonical", content: strings.ReplaceAll(strings.ReplaceAll(valid, "\n", ""), "  ", "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := (releaseMetadata{}).validateReleaseMetadata([]byte(test.content)); err == nil {
				t.Fatal("validateReleaseMetadata() error = nil")
			}
		})
	}
	if err := (releaseMetadata{}).checkReleaseMetadata(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing metadata error = %v", err)
	}
}
