package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// moduleVerifier owns module, generated-source, release-artifact, and tree-integrity checks.
type moduleVerifier struct{}

func (owner moduleVerifier) checkModule(ctx context.Context, root string) error {
	if err := (commandRunner{}).command(ctx, root, nil, "go", "mod", "tidy", "-diff"); err != nil {
		return err
	}
	if err := (commandRunner{}).command(ctx, root, nil, "go", "-C", "tools", "mod", "tidy", "-diff"); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "spice-agent-coding-vendor-*")
	if err != nil {
		return err
	}
	defer (commandRunner{}).removeTree(temporary)
	candidate := filepath.Join(temporary, "vendor")
	if vendorErr := (commandRunner{}).command(ctx, root, nil, "go", "mod", "vendor", "-o", candidate); vendorErr != nil {
		return vendorErr
	}
	current, err := (moduleVerifier{}).treeDigests(filepath.Join(root, "vendor"))
	if err != nil {
		return err
	}
	expected, err := (moduleVerifier{}).treeDigests(candidate)
	if err != nil {
		return err
	}
	if !maps.Equal(current, expected) {
		return errors.New("vendor differs from a fresh go mod vendor result")
	}
	return nil
}

func (owner moduleVerifier) checkGeneratedApplications(ctx context.Context, root string) error {
	offlineVendor := map[string]string{"GOFLAGS": "-mod=vendor"}
	for _, arguments := range (moduleVerifier{}).generatedApplicationChecks() {
		if err := (commandRunner{}).command(ctx, root, offlineVendor, "go", arguments...); err != nil {
			return err
		}
	}
	if err := (commandRunner{}).command(ctx, root, offlineVendor, "go", "run", "./internal/releaseassets", "--check"); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "spice-agent-build-*")
	if err != nil {
		return err
	}
	defer (commandRunner{}).removeTree(temporary)
	for _, executable := range []struct {
		name       string
		packageDir string
	}{
		{name: "spice-agentd", packageDir: "spice-agentd"},
		{name: "spice-agent", packageDir: "spice-agent"},
	} {
		outputName := executable.name
		if runtime.GOOS == "windows" {
			outputName += ".exe"
		}
		if err = (commandRunner{}).command(
			ctx, root, offlineVendor, "go", "build", "-trimpath",
			"-o", filepath.Join(temporary, outputName), "./cmd/"+executable.packageDir,
		); err != nil {
			return err
		}
	}
	return nil
}

func (owner moduleVerifier) verifyReleaseArtifacts(ctx context.Context, root, directory string) error {
	normalized, err := (releaseArtifactPath{}).normalizeReleaseArtifactDirectory(directory)
	if err != nil {
		return err
	}
	environment := map[string]string{
		"GOFLAGS": "-mod=vendor", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local",
	}
	return (commandRunner{}).command(ctx, root, environment, "go", (moduleVerifier{}).releaseArtifactTestArguments(root, normalized)...)
}

func (owner moduleVerifier) verifyLiveRelease(ctx context.Context, root, directory string) error {
	normalized, err := (releaseArtifactPath{}).normalizeReleaseArtifactDirectory(directory)
	if err != nil {
		return err
	}
	environment, err := (moduleVerifier{}).liveReleaseEnvironment(runtime.GOOS, os.LookupEnv)
	if err != nil {
		return err
	}
	environment["GOFLAGS"] = "-mod=vendor"
	environment["GOPROXY"] = "off"
	environment["GOSUMDB"] = "off"
	environment["GOTOOLCHAIN"] = "local"
	return (commandRunner{}).command(
		ctx, root, environment, "go",
		(moduleVerifier{}).liveReleaseTestArguments(root, normalized)...,
	)
}

func (owner moduleVerifier) releaseArtifactTestArguments(root, directory string) []string {
	return []string{
		"test", "-tags=spice_release_artifacts", "-count=1",
		"-run=^TestVerifiedReleasePhase6$",
		"./internal/installedacceptance",
		"-args", "-spice-release-candidate-root=" + root,
		"-spice-release-artifact-dir=" + directory,
	}
}

func (owner moduleVerifier) liveReleaseTestArguments(root, directory string) []string {
	return []string{
		"test", "-tags=spice_release_artifacts,spice_release_live", "-count=1",
		"-run=^TestVerifiedLiveReleaseWorkflow$", "./internal/installedacceptance",
		"-args", "-spice-release-candidate-root=" + root,
		"-spice-release-artifact-dir=" + directory,
	}
}

func (owner moduleVerifier) liveReleaseEnvironment(
	goos string,
	lookup func(string) (string, bool),
) (map[string]string, error) {
	if lookup == nil {
		return nil, errors.New("live release environment lookup is required")
	}
	acknowledgement, _ := lookup("SPICE_DISTRIBUTION_LIVE_PROVIDER")
	if acknowledgement != "1" {
		return nil, errors.New("live release verification requires SPICE_DISTRIBUTION_LIVE_PROVIDER=1")
	}
	result := map[string]string{"SPICE_DISTRIBUTION_LIVE_PROVIDER": "1"}
	for _, required := range []string{"OPENAI_API_KEY", "OPENAI_MODEL"} {
		value, found := lookup(required)
		if !found || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("live release verification requires %s", required)
		}
		result[required] = value
	}
	for _, optional := range []string{"OPENAI_BASE_URL", "OPENAI_ORGANIZATION", "OPENAI_PROJECT"} {
		if value, found := lookup(optional); found && strings.TrimSpace(value) != "" {
			result[optional] = value
		}
	}
	acknowledgement, found := lookup("SPICE_DISTRIBUTION_EPHEMERAL_RUNNER")
	if goos == "windows" {
		if !found || acknowledgement != "1" {
			return nil, errors.New(
				"Windows live release verification requires SPICE_DISTRIBUTION_EPHEMERAL_RUNNER=1",
			)
		}
		result["SPICE_DISTRIBUTION_EPHEMERAL_RUNNER"] = "1"
	} else if found && acknowledgement != "" {
		return nil, fmt.Errorf(
			"%s live release verification requires empty SPICE_DISTRIBUTION_EPHEMERAL_RUNNER",
			goos,
		)
	}
	return result, nil
}

func (owner moduleVerifier) generatedApplicationChecks() [][]string {
	const spiceTool = "github.com/spice-framework/toolchain/cmd/spice"
	result := make([][]string, 0, 6)
	for _, target := range []struct {
		name   string
		source string
	}{
		{name: "ArchitectureProof", source: "./internal/architectureproof"},
		{name: "spice-agentd", source: "./cmd/spice-agentd"},
		{name: "spice-agent", source: "./cmd/spice-agent"},
	} {
		for _, mode := range []string{"--check", "--diff"} {
			result = append(result, []string{
				"tool", spiceTool, "generate", mode, "--target", target.name, ".", target.source,
			})
		}
	}
	return result
}

func (owner moduleVerifier) treeDigests(root string) (map[string][sha256.Size]byte, error) {
	result := make(map[string][sha256.Size]byte)
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	return (moduleVerifier{}).digests(root, false)
}

func (owner moduleVerifier) sourceTreeDigests(root string) (map[string][sha256.Size]byte, error) {
	return (moduleVerifier{}).digests(root, true)
}

func (owner moduleVerifier) digests(root string, excludeGit bool) (map[string][sha256.Size]byte, error) {
	result := make(map[string][sha256.Size]byte)
	opened, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer opened.Close() //nolint:errcheck // Read-only close cannot affect verification.
	err = fs.WalkDir(opened.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if excludeGit && path == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		content, readErr := opened.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		result[filepath.ToSlash(path)] = sha256.Sum256(content)
		return nil
	})
	return result, err
}
