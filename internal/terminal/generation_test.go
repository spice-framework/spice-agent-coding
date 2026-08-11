package terminal_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/spice_agent"
	"github.com/spice-framework/spice-agent-coding/internal/testpath"
	agenttui "github.com/spice-framework/spice-agent-tui"
	spicebean "github.com/spice-framework/spice/bean"
	spiceconfig "github.com/spice-framework/spice/config"
)

const terminalSpiceTool = "github.com/spice-framework/toolchain/cmd/spice"

func TestTerminalGenerationAndBeanExplanationAreCurrent(t *testing.T) {
	root := terminalRepositoryRoot(t)
	for _, retired := range []string{
		filepath.Join(root, ".spice", "terminal.manifest.json"),
		filepath.Join(root, "internal", "spicegen", "terminal"),
	} {
		if _, err := os.Stat(retired); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retired terminal generation path %q still exists: %v", retired, err)
		}
	}
	for _, arguments := range [][]string{
		{"generate", "--check", "--target", "spice-agent", "./cmd/spice-agent"},
		{"generate", "--diff", "--target", "spice-agent", "./cmd/spice-agent"},
	} {
		stdout, stderr, err := runTerminalSpice(t, root, arguments...)
		if err != nil || stderr != "" || !strings.Contains(stdout, "generation is current") {
			t.Fatalf("spice %v = stdout %q, stderr %q, error %v", arguments, stdout, stderr, err)
		}
	}
	stdout, stderr, err := runTerminalSpice(
		t, root, "beans", "--explain", "--format=json", "./internal/terminal",
	)
	if err != nil || stderr != "" {
		t.Fatalf("spice beans = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	for _, expected := range []string{
		`"name": "terminalEndpointStore"`,
		`"name": "terminalManagedConnector"`,
		`"name": "terminalClientConnector"`,
		`"name": "terminalSession"`,
		`"name": "terminalShell"`,
		`"name": "fixedRenderer"`,
		`"name": "darkTheme"`,
		`"fallback": true`,
		`"module": "github.com/spice-framework/spice-agent-tui"`,
	} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("bean explanation lacks %q: %s", expected, stdout)
		}
	}
}

func TestGeneratedTerminalConstructsInspectableGraphWithoutConnecting(t *testing.T) {
	streams, err := agenttui.NewTerminalIO(bytes.NewReader(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	values, err := spiceconfig.NewMapSource("test", map[string]string{
		"agent.workspace":     t.TempDir(),
		"agent.terminal.mode": "check",
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := spicegen.NewApplicationWithOptions(
		context.Background(),
		spicegen.ApplicationOptions{
			Sources: []spiceconfig.Source{values},
			Overrides: spicegen.BeanOverrides{
				OsTerminalIO: spicebean.Replace(streams),
			},
		},
	)
	if err != nil {
		t.Fatalf("construct generated terminal: %v", err)
	}
	components := application.Components()
	if components.TerminalEndpointStore == nil || components.TerminalManagedDiscovery == nil ||
		components.TerminalStartupLock == nil || components.TerminalDaemonStarter == nil ||
		components.TerminalManagedConnector == nil || components.TerminalClientConnector == nil ||
		components.TerminalSession == nil || components.TerminalShell == nil ||
		components.FixedRenderer == nil || components.DarkTheme == nil {
		t.Fatal("generated terminal graph is incomplete")
	}
	if components.TerminalClientConnector != components.TerminalManagedConnector {
		t.Fatal("check mode did not select the generated managed connector")
	}
	if application.Logger() == nil || application.LoggingController() == nil {
		t.Fatal("generated terminal did not expose its instance-owned core logger")
	}
	if components.Properties.TerminalMode != "check" ||
		components.TerminalWorkspace.Title().String() == "" ||
		components.TerminalInitialStatus.Level() != agenttui.StatusReconnecting {
		t.Fatal("generated typed terminal configuration was not injected")
	}
	stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = application.Stop(stopContext); err != nil {
		t.Fatalf("stop generated terminal: %v", err)
	}
}

func TestGeneratedTerminalIsDirectAndSourceMapped(t *testing.T) {
	root := terminalRepositoryRoot(t)
	providers, err := os.ReadFile(filepath.Join(root, "internal", "spicegen", "spice_agent", "spice_providers_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"ConstructTerminalEndpointStore", "ConstructTerminalManagedConnector",
		"ConstructTerminalClientConnector", "ConstructTerminalSession",
		"ConstructTerminalShell",
	} {
		if !bytes.Contains(providers, []byte(expected)) {
			t.Fatalf("generated provider file lacks %q", expected)
		}
	}
	for _, forbidden := range []string{"reflect.", "RuntimeGraph", "ServiceLocator", "ExtensionRegistry"} {
		if bytes.Contains(providers, []byte(forbidden)) {
			t.Fatalf("generated provider file contains forbidden %q", forbidden)
		}
	}
	if bytes.Contains(providers, []byte("agentLogging")) ||
		bytes.Contains(providers, []byte("spice-agent/logging")) {
		t.Fatal("terminal graph subscribed to daemon Agent event logging")
	}
	manifest, err := os.ReadFile(filepath.Join(root, ".spice", "spice_agent.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"id": "spice_agent"`,
		`"layout": "application-package"`,
		`"entrypoint_package": "github.com/spice-framework/spice-agent-coding/cmd/spice-agent"`,
		`"bridge_dir": "cmd/spice-agent"`, `"role": "source-unit"`,
		"cmd/spice-agent/application.go",
		"internal/terminal/terminal_client_connector_bean.go",
		"internal/terminal/terminal_endpoint_store_bean.go",
		"internal/terminal/properties.go",
		"internal/terminal/terminal_session_bean.go",
		"github.com/spice-framework/spice-agent-tui/autoconfigure/autoconfigure.go",
	} {
		if !bytes.Contains(manifest, []byte(expected)) {
			t.Fatalf("generation manifest lacks %q", expected)
		}
	}
	if bytes.Contains(manifest, []byte("spice-agent/logging/autoconfigure")) {
		t.Fatal("terminal manifest contains daemon Agent logging auto-configuration")
	}
}

func runTerminalSpice(t *testing.T, root string, arguments ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	commandArguments := append([]string{"tool", terminalSpiceTool}, arguments...)
	// #nosec G204,G702 -- executable and arguments are fixed terminal generation checks.
	command := exec.CommandContext(ctx, terminalGoExecutable(), commandArguments...)
	command.Dir = root
	command.Env = testpath.Environment(map[string]string{
		"GOFLAGS": "-mod=vendor", "GOPROXY": "off", "GOTOOLCHAIN": "local", "GOWORK": "off",
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() != nil {
		t.Fatalf("spice %v timed out: %v", arguments, ctx.Err())
	}
	return stdout.String(), stderr.String(), err
}

func terminalGoExecutable() string {
	name := "go"
	if runtime.GOOS == "windows" {
		name = "go.exe"
	}
	return filepath.Join(runtime.GOROOT(), "bin", name) //nolint:staticcheck // Use the exact executing Go toolchain.
}

func terminalRepositoryRoot(t *testing.T) string {
	t.Helper()
	current, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		content, readErr := os.ReadFile(filepath.Join(current, "go.mod"))
		if readErr == nil && bytes.Contains(content, []byte("module github.com/spice-framework/spice-agent-coding\n")) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatal("locate spice-agent-coding module root")
		}
		current = parent
	}
}
