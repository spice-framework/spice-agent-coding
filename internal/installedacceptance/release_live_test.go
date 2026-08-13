//go:build spice_release_artifacts && spice_release_live

package installedacceptance

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Kodecable/crosspty"
	"github.com/spice-framework/spice-agent-tui/tuittest"
)

const (
	liveReleaseAcknowledgement = "SPICE_DISTRIBUTION_LIVE_PROVIDER"
	liveReleaseFinalMarker     = "LIVE-SPICE-PHASE6-COMPLETE"
	liveReleaseOutput          = "live-phase6-output"
	liveReleaseTimeout         = 2 * time.Minute
)

func TestVerifiedLiveReleaseWorkflow(t *testing.T) {
	if os.Getenv(liveReleaseAcknowledgement) != "1" {
		t.Fatalf("%s=1 is required", liveReleaseAcknowledgement)
	}
	for _, name := range []string{"OPENAI_API_KEY", "OPENAI_MODEL"} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			t.Fatalf("%s is required", name)
		}
	}
	set := verifiedReleaseSet(t)
	installRoot, err := set.ExtractNative(
		filepath.Join(t.TempDir(), "live π installed bytes"), runtime.GOOS, runtime.GOARCH,
	)
	if err != nil {
		t.Fatal(err)
	}
	daemonBinary := filepath.Join(installRoot, executableName("spice-agentd"))
	terminalBinary := filepath.Join(installRoot, executableName("spice-agent"))
	workspace := t.TempDir()
	if err = os.WriteFile(filepath.Join(workspace, "README.md"), []byte("live phase6 input\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := repositoryRoot(t)
	plugin := buildOfflineTestBinary(t, root, "spice-agent-distribution-fixture", "./testdata/runtimeplugin/go")
	store, environment := releaseProcessEnvironment(t)
	delete(environment, "OPENAI_BASE_URL")
	delete(environment, "OPENAI_ORGANIZATION")
	delete(environment, "OPENAI_PROJECT")
	for _, name := range []string{
		"OPENAI_API_KEY", "OPENAI_MODEL", "OPENAI_BASE_URL", "OPENAI_ORGANIZATION", "OPENAI_PROJECT",
	} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			environment[name] = value
		}
	}
	environment["OPENAI_MAX_RETRIES"] = "0"
	environment["OPENAI_TIMEOUT"] = "90s"
	environment["SPICE_AGENT_WORKSPACE"] = workspace
	authorityDirectory := t.TempDir()
	environment["SPICE_AGENT_RUN_AUTHORITY_DIRECTORY"] = authorityDirectory
	configureAcceptancePlugin(environment, plugin, fileSHA256(t, plugin))
	environment["SPICE_AGENT_RUNTIME_PLUGIN_STARTUP_TIMEOUT"] = "30s"

	daemon := startProcess(t, daemonBinary, []string{"serve"}, environment)
	t.Cleanup(func() { daemon.stop(t, false) })
	metadata := waitForEndpoint(t, store, daemon, nil, "")
	assertReleasedServer(t, metadata, set)
	terminal := startReleasedNativeTerminal(
		t, terminalBinary, []string{"attach", "--endpoint", metadata.Address()},
		releasedNativeTerminalEnvironment(t, environment),
		crosspty.KillModeKillGroupOnSubProcessExit,
	)
	waitForLiveReleaseScreen(t, terminal, func(screen tuittest.Screen) bool {
		return screen.AlternateScreen() && screen.Contains("Spice Agent")
	})
	prompt := "Use read on README.md; use replace to create live-output.txt with exact content " +
		liveReleaseOutput + "; use shell to print live-shell; use fixture.echo with value live-plugin; " +
		"then respond exactly " + liveReleaseFinalMarker
	if err = terminal.write([]byte(prompt + "\r")); err != nil {
		t.Fatal(err)
	}
	final := waitForLiveReleaseScreen(t, terminal, func(screen tuittest.Screen) bool {
		return screen.Contains(liveReleaseFinalMarker)
	})
	if strings.Count(final.Plain(), liveReleaseFinalMarker) != 1 {
		t.Fatalf("live final marker was not exactly once:\n%s", final.Plain())
	}
	content, err := os.ReadFile(filepath.Join(workspace, "live-output.txt"))
	if err != nil || strings.TrimSpace(string(content)) != liveReleaseOutput {
		t.Fatalf("live replace output = %q, error %v", content, err)
	}
	transcript := string(terminal.transcript())
	metadataDiagnostic := fmt.Sprintf("%v", metadata)
	quitReleasedNativeTerminal(t, terminal)
	daemon.stop(t, true)
	assertEndpointAbsent(t, store, daemon)
	roots := []string{workspace, authorityDirectory}
	for _, name := range []string{"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "HOME"} {
		if value := strings.TrimSpace(environment[name]); value != "" {
			roots = append(roots, value)
		}
	}
	assertLiveSecretAbsent(t, os.Getenv("OPENAI_API_KEY"), map[string]string{
		"terminal transcript": transcript,
		"daemon stdout":       daemon.stdout.String(),
		"daemon stderr":       daemon.stderr.String(),
		"endpoint metadata":   metadataDiagnostic,
	}, roots)
}

func assertLiveSecretAbsent(
	t *testing.T,
	secret string,
	visible map[string]string,
	roots []string,
) {
	t.Helper()
	if err := scanLiveSecret(secret, visible, roots); err != nil {
		t.Fatal(err)
	}
}

func scanLiveSecret(secret string, visible map[string]string, roots []string) error {
	if secret == "" {
		return errors.New("live secret scan requires a nonempty canary")
	}
	for name, content := range visible {
		if strings.Contains(content, secret) {
			return fmt.Errorf("live provider credential persisted in %s", name)
		}
	}
	const (
		maximumFiles = 1_000
		maximumBytes = 16 << 20
	)
	files := 0
	bytesRead := int64(0)
	for _, root := range roots {
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("secret-scan root contains symlink %q", path)
			}
			files++
			if files > maximumFiles {
				return errors.New("secret-scan file bound exceeded")
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			bytesRead += info.Size()
			if bytesRead > maximumBytes {
				return errors.New("secret-scan byte bound exceeded")
			}
			content, err := os.ReadFile(path) // #nosec G304 -- bounded beneath exact test-owned roots.
			if err != nil {
				return err
			}
			if strings.Contains(string(content), secret) {
				return fmt.Errorf("live provider credential persisted beneath test-owned root %q", root)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func TestLiveSecretScanIsBoundedAndFailClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "safe.txt"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := scanLiveSecret("canary", map[string]string{"stdout": "safe"}, []string{root}); err != nil {
		t.Fatal(err)
	}
	for name, prepare := range map[string]func() (map[string]string, []string){
		"visible output": func() (map[string]string, []string) {
			return map[string]string{"stderr": "prefix-canary-suffix"}, []string{root}
		},
		"persisted file": func() (map[string]string, []string) {
			persisted := t.TempDir()
			if err := os.WriteFile(filepath.Join(persisted, "endpoint.json"), []byte("canary"), 0o600); err != nil {
				t.Fatal(err)
			}
			return nil, []string{persisted}
		},
	} {
		t.Run(name, func(t *testing.T) {
			visible, roots := prepare()
			if err := scanLiveSecret("canary", visible, roots); err == nil {
				t.Fatal("secret scan accepted persisted credential")
			}
		})
	}
}

func waitForLiveReleaseScreen(
	t *testing.T,
	terminal *nativeTerminal,
	predicate func(tuittest.Screen) bool,
) tuittest.Screen {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), liveReleaseTimeout)
	defer cancel()
	screen, err := terminal.waitFor(ctx, "live-release", predicate)
	if err != nil {
		t.Fatalf("wait for live release workflow: %v\ntranscript tail:\n%s", err, terminal.transcriptDiagnostic())
	}
	return screen
}
