//go:build spice_release_artifacts

package installedacceptance

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spice-framework/spice-agent-coding/internal/releaseinstallation"
)

type releaseInstallTarget struct {
	goos   string
	goarch string
}

type installedReleaseFile struct {
	mode   fs.FileMode
	size   int64
	digest [sha256.Size]byte
}

// TestVerifiedReleasePhase6 is the single deterministic installed-release
// contract. Keeping these proofs under one entrypoint prevents a caller from
// measuring or exercising the decisive client flow without also executing all
// six archive installations and the native replacement/history PTY proof.
func TestVerifiedReleasePhase6(t *testing.T) {
	t.Run("all six archives install deterministically", verifyAllReleaseArchivesInstallDeterministically)
	t.Run("native released behavior", verifyNativeReleaseArchive)
	t.Run("decisive client workflow", verifyDecisiveReleaseWorkflow)
	t.Run("installed performance", verifyInstalledPerformanceSamples)
}

func verifyAllReleaseArchivesInstallDeterministically(t *testing.T) {
	set := verifiedReleaseSet(t)
	targets := []releaseInstallTarget{
		{goos: "darwin", goarch: "amd64"},
		{goos: "darwin", goarch: "arm64"},
		{goos: "linux", goarch: "amd64"},
		{goos: "linux", goarch: "arm64"},
		{goos: "windows", goarch: "amd64"},
		{goos: "windows", goarch: "arm64"},
	}
	for _, target := range targets {
		name := target.goos + "_" + target.goarch
		t.Run(name, func(t *testing.T) {
			firstRoot := extractReleaseTarget(
				t, set, filepath.Join(t.TempDir(), "first π install "+name), target,
			)
			secondRoot := extractReleaseTarget(
				t, set, filepath.Join(t.TempDir(), "second 界 install "+name), target,
			)
			first := installedReleaseTree(t, firstRoot)
			second := installedReleaseTree(t, secondRoot)
			if !installedReleaseTreesEqual(first, second) {
				t.Fatalf("%s installation differs across clean extraction roots\nfirst: %#v\nsecond: %#v", name, first, second)
			}
			want := []string{
				"LICENSE", "README.md", "THIRD_PARTY_NOTICES.md",
				"docs/configuration.md", "docs/installation.md", "docs/security.md",
				"protocol-descriptors.pb", "spice-agent", "spice-agentd",
			}
			if target.goos == "windows" {
				want[7], want[8] = "spice-agent.exe", "spice-agentd.exe"
			}
			slices.Sort(want)
			got := make([]string, 0, len(first))
			for path := range first {
				got = append(got, path)
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("%s installed membership = %q, want %q", name, got, want)
			}
		})
	}
}

func verifiedReleaseSet(t *testing.T) *releaseinstallation.Set {
	t.Helper()
	if verifiedArtifactDirectory == nil || *verifiedArtifactDirectory == "" {
		t.Fatal("-spice-release-artifact-dir is required")
	}
	if releaseCandidateRoot == nil || *releaseCandidateRoot == "" {
		t.Fatal("-spice-release-candidate-root is required")
	}
	set, err := releaseinstallation.NewVerifier().VerifyCandidate(
		*releaseCandidateRoot,
		*verifiedArtifactDirectory,
	)
	if err != nil {
		t.Fatalf("validate independently verified release subjects: %v", err)
	}
	return set
}

func extractReleaseTarget(
	t *testing.T,
	set *releaseinstallation.Set,
	destination string,
	target releaseInstallTarget,
) string {
	t.Helper()
	root, err := set.ExtractNative(destination, target.goos, target.goarch)
	if err != nil {
		t.Fatalf("extract %s/%s release archive: %v", target.goos, target.goarch, err)
	}
	return root
}

func installedReleaseTree(t *testing.T, root string) map[string]installedReleaseFile {
	t.Helper()
	result := make(map[string]installedReleaseFile)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("installed entry %q is not a physical regular file", path)
		}
		content, err := os.ReadFile(path) // #nosec G304 -- bounded beneath an exact test-owned extraction root.
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." || strings.HasPrefix(relative, "../") {
			return fmt.Errorf("installed entry %q escapes root", path)
		}
		result[relative] = installedReleaseFile{
			mode: info.Mode().Perm(), size: info.Size(), digest: sha256.Sum256(content),
		}
		return nil
	})
	if err != nil {
		t.Fatalf("inspect installed release tree: %v", err)
	}
	return result
}

func installedReleaseTreesEqual(
	left, right map[string]installedReleaseFile,
) bool {
	if len(left) != len(right) {
		return false
	}
	for path, expected := range left {
		if actual, found := right[path]; !found || actual != expected {
			return false
		}
	}
	return true
}
