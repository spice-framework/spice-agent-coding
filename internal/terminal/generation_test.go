package terminal_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	spicegen "github.com/spice-framework/spice-agent-coding/internal/spicegen/terminal"
	agenttui "github.com/spice-framework/spice-agent-tui"
	spicebean "github.com/spice-framework/spice/bean"
	spiceconfig "github.com/spice-framework/spice/config"
)

const terminalSpiceTool = "github.com/spice-framework/toolchain/cmd/spice"

func TestTerminalGenerationAndBeanExplanationAreCurrent(t *testing.T) {
	root := terminalRepositoryRoot(t)
	for _, arguments := range [][]string{
		{"generate", "--check", "--target", "Terminal", ".", "./internal/terminal"},
		{"generate", "--diff", "--target", "Terminal", ".", "./internal/terminal"},
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
	providers, err := os.ReadFile(filepath.Join(root, "internal", "spicegen", "terminal", "spice_providers_gen.go"))
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
	manifest, err := os.ReadFile(filepath.Join(root, ".spice", "terminal.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"layout": "generated-package"`, `"role": "source-unit"`,
		"internal/terminal/application.go", "internal/terminal/connection.go",
		"internal/terminal/endpoint.go", "internal/terminal/properties.go",
		"internal/terminal/session.go",
		"github.com/spice-framework/spice-agent-tui/autoconfigure/autoconfigure.go",
	} {
		if !bytes.Contains(manifest, []byte(expected)) {
			t.Fatalf("generation manifest lacks %q", expected)
		}
	}
}

func runTerminalSpice(t *testing.T, root string, arguments ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	commandArguments := append([]string{"tool", terminalSpiceTool}, arguments...)
	// #nosec G204,G702 -- executable and arguments are fixed terminal generation checks.
	command := exec.CommandContext(ctx, terminalGoExecutable(), commandArguments...)
	command.Dir = root
	command.Env = append(
		os.Environ(),
		"GOWORK=off", "GOPROXY=off", "GOFLAGS=-mod=vendor", "GOTOOLCHAIN=local",
	)
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
