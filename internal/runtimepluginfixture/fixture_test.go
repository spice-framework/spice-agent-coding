package runtimepluginfixture_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent-coding/internal/processplatform"
	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
	"github.com/spice-framework/spice-agent/plugin/host/localendpoint"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"github.com/spice-framework/spice-agent/stage"
	"github.com/spice-framework/spice-agent/tool"
)

const (
	fixtureManifest = "spice-agent-distribution-fixture"
	fixtureVersion  = "v1"
	fixtureTool     = "fixture.echo"
)

func TestOfflineFixtureActivatesAndShutsDownThroughProductionHost(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	executablePath, digest := buildFixture(t, root)

	launcher, err := processplatform.NewLauncher(acceptingRegistrar{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := stage.NewDispatcher(map[string]tool.Tool{})
	if err != nil {
		t.Fatal(err)
	}
	host, err := pluginhost.NewHost(pluginhost.HostConfig{
		HostIdentity: &pluginv1.BuildIdentity{
			Component: "spice-agentd-fixture-host", Version: "v1",
			Commit: "fixture", Runtime: runtime.Version(),
		},
		Compiled: compiled, Processes: launcher, Endpoints: localendpoint.NewFactory(),
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := host.Close(ctx); closeErr != nil {
			t.Errorf("close fixture host during cleanup: %v", closeErr)
		}
	})

	digestValue, err := pluginhost.ParseSHA256(digest)
	if err != nil {
		t.Fatalf("parse fixture digest: %v", err)
	}
	executable, err := pluginhost.NewExecutable(pluginhost.ExecutableConfig{
		ID: "distribution-fixture", ManifestName: fixtureManifest,
		ManifestVersion: fixtureVersion, Path: executablePath, SHA256: digestValue,
		WorkingDirectory: filepath.Dir(executablePath), Environment: []string{},
		RequestedLimits: fixtureLimits(), StartupTimeout: 5 * time.Second,
		CallTimeout: 5 * time.Second, DrainTimeout: 5 * time.Second,
		ShutdownTimeout: 5 * time.Second, ContainmentTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("construct fixture executable: %v", err)
	}
	set, err := pluginhost.NewSet([]pluginhost.Executable{executable})
	if err != nil {
		t.Fatal(err)
	}
	activationContext, cancelActivation := context.WithTimeout(t.Context(), 10*time.Second)
	planID, err := host.Activate(activationContext, set)
	cancelActivation()
	if err != nil {
		t.Fatalf("activate fixture: %v", err)
	}
	if err = planID.Validate(); err != nil {
		t.Fatalf("activated plan identity: %v", err)
	}

	lease, err := host.LeaseCurrent(t.Context())
	if err != nil {
		t.Fatalf("lease fixture generation: %v", err)
	}
	definitions := lease.Definitions()
	if len(definitions) != 1 || definitions[0].Name() != fixtureTool ||
		definitions[0].Effect() != tool.EffectReadOnly ||
		definitions[0].ReplaySafety() != tool.ReplaySafe ||
		len(definitions[0].Capabilities()) != 0 {
		t.Fatalf("fixture definitions = %#v", definitions)
	}
	call, err := tool.NewCall("distribution-fixture-call", fixtureTool, []byte(`{"value":"offline"}`))
	if err != nil {
		t.Fatal(err)
	}
	reporter := &recordingReporter{}
	result, err := lease.Dispatcher().Dispatch(t.Context(), call, reporter)
	if err != nil {
		t.Fatalf("dispatch fixture echo: %v", err)
	}
	if string(result.Content()) != `{"value":"offline"}` {
		t.Fatalf("fixture result = %s", result.Content())
	}
	if got := reporter.messages(); len(got) != 1 || got[0] != "echo accepted" {
		t.Fatalf("fixture progress = %#v", got)
	}
	if err = lease.Release(); err != nil {
		t.Fatalf("release fixture generation: %v", err)
	}

	closeContext, cancelClose := context.WithTimeout(context.Background(), 10*time.Second)
	err = host.Close(closeContext)
	cancelClose()
	if err != nil {
		t.Fatalf("drain and shut down fixture: %v", err)
	}
	closed = true
	health := host.Health()
	if err = health.Validate(); err != nil || health.State() != pluginhost.HealthStateStopped ||
		health.ActiveLeases() != 0 || health.RetainedGenerations() != 0 {
		t.Fatalf("closed fixture host health = %s, validation = %v", health, err)
	}
}

func TestFixtureSourceUsesOnlyPublicSpiceAgentPackages(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	fixtureRoot := filepath.Join(root, "testdata", "runtimeplugin", "go")
	entries, err := os.ReadDir(fixtureRoot)
	if err != nil {
		t.Fatal(err)
	}
	stdoutReferences := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(fixtureRoot, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		stdoutReferences += bytes.Count(content, []byte("os.Stdout"))
		for _, forbidden := range []string{
			"github.com/spice-framework/spice-agent/internal/",
			"fmt.Print(", "fmt.Printf(", "fmt.Println(", "log.Print",
		} {
			if bytes.Contains(content, []byte(forbidden)) {
				t.Fatalf("fixture source %s contains forbidden %q", entry.Name(), forbidden)
			}
		}
	}
	if stdoutReferences != 1 {
		t.Fatalf("fixture stdout references = %d, want exact readiness writer", stdoutReferences)
	}
	mainSource, err := os.ReadFile(filepath.Join(fixtureRoot, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(mainSource, []byte("pluginv1.WriteReadiness(os.Stdout)")) {
		t.Fatal("fixture stdout is not owned exclusively by the protocol readiness helper")
	}
}

type acceptingRegistrar struct{}

func (acceptingRegistrar) Register(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return errors.New("fixture process registration is invalid")
	}
	return nil
}

type recordingReporter struct {
	mu       sync.Mutex
	progress []string
}

func (reporter *recordingReporter) Report(ctx context.Context, progress tool.Progress) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	reporter.progress = append(reporter.progress, progress.Message())
	return nil
}

func (reporter *recordingReporter) messages() []string {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	return append([]string(nil), reporter.progress...)
}

func buildFixture(t *testing.T, root string) (string, string) {
	t.Helper()
	name := "spice-agent-distribution-fixture"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext( // #nosec G204,G702 -- exact Go and fixed fixture package.
		ctx,
		exactGoExecutable(),
		"build", "-mod=vendor", "-trimpath", "-buildvcs=false",
		"-ldflags=-buildid=", "-o", path, "./testdata/runtimeplugin/go",
	)
	command.Dir = root
	command.Env = append(
		os.Environ(),
		"GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("build offline fixture: stdout %q, stderr %q, error %v", stdout.String(), stderr.String(), err)
	}
	if ctx.Err() != nil {
		t.Fatalf("build offline fixture: %v", ctx.Err())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("fixture build emitted output: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	if digest != strings.ToLower(digest) || len(digest) != sha256.Size*2 {
		t.Fatalf("fixture digest is not canonical lowercase SHA-256: %q", digest)
	}
	return path, digest
}

func fixtureLimits() *pluginv1.Limits {
	return &pluginv1.Limits{
		MaxMessageBytes: 64 << 10, MaxTools: 1, MaxSchemaBytes: 4 << 10,
		MaxCallArgumentBytes: 4 << 10, MaxResultBytes: 4 << 10,
		MaxProgressBytes: 256, MaxConcurrentCalls: 4,
	}
}

func exactGoExecutable() string {
	name := "go"
	if runtime.GOOS == "windows" {
		name = "go.exe"
	}
	return filepath.Join(runtime.GOROOT(), "bin", name) //nolint:staticcheck // Select the exact executing toolchain.
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	current, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		content, readErr := os.ReadFile(filepath.Join(current, "go.mod"))
		if readErr == nil && bytes.Contains(
			content,
			[]byte("module github.com/spice-framework/spice-agent-coding\n"),
		) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatal("locate spice-agent-coding module root")
		}
		current = parent
	}
}
