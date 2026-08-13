package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestRenderDescriptorsIsDeterministicAndComplete(t *testing.T) {
	t.Parallel()
	command := releaseAssetsCommand{}
	first, err := command.renderDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	second, err := command.renderDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("descriptor rendering is not byte-identical")
	}
	var set descriptorpb.FileDescriptorSet
	if err = proto.Unmarshal(first, &set); err != nil {
		t.Fatalf("decode descriptor set: %v", err)
	}
	paths := make([]string, 0, len(set.File))
	for _, file := range set.File {
		paths = append(paths, file.GetName())
	}
	if !slices.IsSorted(paths) {
		t.Fatalf("descriptor paths are not sorted: %v", paths)
	}
	for _, required := range []string{
		"spice/agent/common/v1/common.proto",
		"spice/agent/engine/v1/engine.proto",
		"spice/agent/plugin/v1/plugin.proto",
	} {
		if !slices.Contains(paths, required) {
			t.Fatalf("descriptor set lacks %q: %v", required, paths)
		}
	}
}

func TestRenderNoticesUsesSortedVersionedModulesAndLicenseText(t *testing.T) {
	t.Parallel()
	command := releaseAssetsCommand{}
	root := t.TempDir()
	writeFixture(t, root, "vendor/modules.txt", strings.Join([]string{
		"# example.com/zeta v1.2.3",
		"## explicit; go 1.26.0",
		"example.com/zeta/package",
		"# example.com/alpha v0.4.0",
		"## explicit; go 1.26.0",
		"example.com/alpha",
	}, "\n")+"\n")
	writeFixture(t, root, "vendor/example.com/zeta/LICENSE", "zeta license\r\n")
	writeFixture(t, root, "vendor/example.com/alpha/NOTICE.txt", "alpha notice\rsecond line\r")

	first, err := command.renderNotices(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := command.renderNotices(root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("notice rendering is not byte-identical")
	}
	content := string(first)
	if !strings.HasPrefix(content, "<!-- Code generated") ||
		strings.Index(content, "example.com/alpha") > strings.Index(content, "example.com/zeta") ||
		!strings.Contains(content, "    alpha notice\n    second line\n") ||
		strings.Contains(content, "\r") {
		t.Fatalf("unexpected notices:\n%s", content)
	}
}

func TestRenderNoticesIncludesDeterministicSupplementalAttribution(t *testing.T) {
	t.Parallel()
	command := releaseAssetsCommand{}
	root := t.TempDir()
	const module = "github.com/Kodecable/crosspty"
	writeFixture(t, root, "vendor/modules.txt", "# "+module+" v1.1.0\n")
	writeFixture(t, root, "vendor/"+module+"/LICENSE", "root license\n")
	for _, name := range command.supplementalNoticeNames(module) {
		writeFixture(
			t, root,
			"internal/releaseassets/attributions/"+module+"/"+name,
			name+" derived license\r\n",
		)
	}
	notices, err := command.renderNotices(root)
	if err != nil {
		t.Fatal(err)
	}
	content := string(notices)
	if !strings.Contains(content, "vendor/"+module+"/LICENSE") ||
		!strings.Contains(
			content,
			"internal/releaseassets/attributions/"+module+"/LICENSE_ActiveState",
		) || strings.Index(content, "LICENSE_ActiveState derived license") > strings.Index(content, "root license") ||
		strings.Contains(content, "\r") {
		t.Fatalf("unexpected supplemental notices:\n%s", content)
	}
	if err = os.Remove(
		filepath.Join(root, "internal/releaseassets/attributions", module, "LICENSE_go"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err = command.renderNotices(root); err == nil || !strings.Contains(err.Error(), "want exact") {
		t.Fatalf("renderNotices() missing required supplemental attribution error = %v", err)
	}
}

func TestCrossPTYSupplementalAttributionsMatchPinnedUpstreamBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		digest string
	}{
		{name: "LICENSE_ActiveState", digest: "9abff047e73764a9b9ee4208cbe4d22ef4844c16d1db70e853aebc292acb47e8"},
		{name: "LICENSE_go", digest: "22975ebe413674bc619428a26e53412013dccffa69e2f27100dbd8d946fde287"},
		{name: "LICENSE_photostorm", digest: "2dfaa7e6ac954dad2163d7e364a1adbed1f6b909e9d743131959306a51621c77"},
	}
	command := releaseAssetsCommand{}
	names := command.supplementalNoticeNames("github.com/Kodecable/crosspty")
	for index, test := range tests {
		if names[index] != test.name {
			t.Fatalf("supplemental attribution %d = %q, want %q", index, names[index], test.name)
		}
		content, err := os.ReadFile(filepath.Join(
			"attributions", "github.com", "Kodecable", "crosspty", test.name,
		))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		if got := hex.EncodeToString(digest[:]); got != test.digest {
			t.Fatalf("%s digest = %s, want pinned upstream %s", test.name, got, test.digest)
		}
	}
}

func TestParseVendorModulesRejectsInvalidAndDuplicatePaths(t *testing.T) {
	t.Parallel()
	command := releaseAssetsCommand{}
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "traversal", content: "# ../../escape v1.0.0\n"},
		{name: "absolute", content: "# /absolute v1.0.0\n"},
		{name: "windows", content: "# C:/absolute v1.0.0\n"},
		{name: "backslash", content: "# example.com\\escape v1.0.0\n"},
		{name: "duplicate", content: "# example.com/module v1.0.0\n# example.com/module v1.0.1\n"},
		{name: "empty", content: "## explicit; go 1.26.0\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "modules.txt")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := command.parseVendorModules(path); err == nil {
				t.Fatal("parseVendorModules() error = nil")
			}
		})
	}
}

func TestApplyCheckDetectsMissingAndStaleAssets(t *testing.T) {
	t.Parallel()
	command := releaseAssetsCommand{}
	root := t.TempDir()
	writeFixture(t, root, "vendor/modules.txt", "# example.com/module v1.0.0\n")
	writeFixture(t, root, "vendor/example.com/module/LICENSE", "fixture license\n")
	if err := command.apply(root, true); err == nil {
		t.Fatal("apply(check) accepted missing assets")
	}
	if err := command.apply(root, false); err != nil {
		t.Fatal(err)
	}
	if err := command.apply(root, true); err != nil {
		t.Fatalf("apply(check) after render: %v", err)
	}
	writeFixture(t, root, noticesPath, "stale\n")
	if err := command.apply(root, true); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("apply(check) error = %v", err)
	}
}

func writeFixture(t *testing.T, root, name, content string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
