package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNetworkAllowedOnlyForBootstrap(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"fast", "check", "fmt", "verify", "unknown"} {
		if networkAllowed(mode) {
			t.Fatalf("networkAllowed(%q) = true", mode)
		}
	}
	if !networkAllowed("tools-bootstrap") {
		t.Fatal("networkAllowed(tools-bootstrap) = false")
	}
}

func TestExactGoExecutable(t *testing.T) {
	t.Parallel()
	if goExecutableName("windows") != "go.exe" || goExecutableName("linux") != "go" {
		t.Fatal("go executable name is not platform-correct")
	}
	actualName := filepath.Base(exactGoExecutable())
	if (actualName != "go" && actualName != "go.exe") || filepath.Base(filepath.Dir(exactGoExecutable())) != "bin" ||
		qualityExecutable("go") != exactGoExecutable() ||
		qualityExecutable("gofumpt") != "gofumpt" {
		t.Fatalf("exact Go executable = %q", exactGoExecutable())
	}
}

func TestBootstrapDownloadArguments(t *testing.T) {
	t.Parallel()
	moduleFile := filepath.Join("private", "graph.mod")
	want := "mod download -modfile=" + moduleFile + " all"
	if got := strings.Join(bootstrapDownloadArguments(moduleFile), " "); got != want {
		t.Fatalf("bootstrapDownloadArguments() = %q, want %q", got, want)
	}
}

func TestBootstrapPreservesRepositoryOnSuccessFailureAndCancellation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		runnerErr error
	}{
		{name: "success"},
		{name: "failure", runnerErr: errors.New("download failed")},
		{name: "cancellation", runnerErr: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := bootstrapFixture(t, true)
			before, err := sourceTreeDigests(root)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if errors.Is(test.runnerErr, context.Canceled) {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			var calls [][]string
			runner := func(callContext context.Context, directory string, arguments ...string) error {
				if directory != root && directory != filepath.Join(root, "tools") {
					t.Fatalf("unexpected directory %q", directory)
				}
				calls = append(calls, append([]string(nil), arguments...))
				if errors.Is(test.runnerErr, context.Canceled) {
					return callContext.Err()
				}
				return test.runnerErr
			}
			err = bootstrapDependencies(ctx, root, runner)
			if !errors.Is(err, test.runnerErr) {
				t.Fatalf("bootstrapDependencies() error = %v, want %v", err, test.runnerErr)
			}
			after, digestErr := sourceTreeDigests(root)
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			if len(before) != len(after) {
				t.Fatalf("repository file count changed: %d != %d", len(before), len(after))
			}
			for name, digest := range before {
				if after[name] != digest {
					t.Fatalf("repository file %q changed", name)
				}
			}
			wantCalls := 2
			if test.runnerErr != nil {
				wantCalls = 1
			}
			if len(calls) != wantCalls {
				t.Fatalf("bootstrap calls = %d, want %d", len(calls), wantCalls)
			}
			for _, arguments := range calls {
				if len(arguments) != 4 || arguments[0] != "mod" || arguments[1] != "download" ||
					!strings.HasPrefix(arguments[2], "-modfile=") || arguments[3] != "all" {
					t.Fatalf("unexpected bootstrap arguments: %q", arguments)
				}
				if strings.HasPrefix(strings.TrimPrefix(arguments[2], "-modfile="), root) {
					t.Fatalf("temporary modfile is inside repository: %q", arguments[2])
				}
			}
		})
	}
}

func TestBootstrapDetectsRepositoryMutation(t *testing.T) {
	t.Parallel()
	root := bootstrapFixture(t, false)
	err := bootstrapDependencies(context.Background(), root, func(_ context.Context, directory string, _ ...string) error {
		return os.WriteFile(filepath.Join(directory, "unexpected"), []byte("mutation"), 0o600)
	})
	if err == nil || !strings.Contains(err.Error(), "modified the repository") {
		t.Fatalf("bootstrapDependencies() error = %v", err)
	}
}

func TestBootstrapAllowsMissingToolsModule(t *testing.T) {
	t.Parallel()
	root := bootstrapFixture(t, false)
	calls := 0
	err := bootstrapDependencies(context.Background(), root, func(_ context.Context, _ string, _ ...string) error {
		calls++
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("bootstrapDependencies() = calls %d, error %v", calls, err)
	}
}

func TestBootstrapEnvironmentRejectsCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("SPICE_TEST_TOKEN", "must-not-leak")
	environment := strings.Join(commandEnvironment(true, nil), "\n")
	for _, required := range []string{
		"GOAUTH=off", "GOPROXY=https://proxy.golang.org", "GOSUMDB=sum.golang.org",
	} {
		if !strings.Contains(environment, required) {
			t.Fatalf("bootstrap environment lacks %q:\n%s", required, environment)
		}
	}
	if strings.Contains(environment, "must-not-leak") {
		t.Fatalf("bootstrap environment contains an application credential:\n%s", environment)
	}
}

func bootstrapFixture(t *testing.T, tools bool) string {
	t.Helper()
	root := t.TempDir()
	modules := []string{root}
	if tools {
		modules = append(modules, filepath.Join(root, "tools"))
	}
	for _, directory := range modules {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.com/fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "go.sum"), []byte("fixture sum\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestValidateCompatibility(t *testing.T) {
	t.Parallel()
	valid := `{"schema":1,"go":"1.26.5","spice":null,"spice_toolchain":null,"spice_agent":null,"spice_agent_tui":null,"spice_agent_provider_openai":null,"spice_agent_tools_coding":null}`
	tests := []struct {
		name, content, wantErr string
	}{
		{name: "valid", content: valid},
		{name: "malformed", content: `{`, wantErr: "decode"},
		{name: "unknown", content: strings.Replace(valid, `}`, `,"extra":true}`, 1), wantErr: "unknown field"},
		{name: "trailing", content: valid + `{}`, wantErr: "trailing"},
		{name: "wrong Go", content: strings.Replace(valid, "1.26.5", "1.26.4", 1), wantErr: "explicit null"},
		{name: "premature core", content: strings.Replace(valid, `"spice_agent":null`, `"spice_agent":"v1"`, 1), wantErr: "explicit null"},
		{name: "premature TUI", content: strings.Replace(valid, `"spice_agent_tui":null`, `"spice_agent_tui":"v1"`, 1), wantErr: "explicit null"},
		{name: "missing selection", content: strings.Replace(valid, `,"spice_agent_tui":null`, "", 1), wantErr: "explicit null"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateCompatibility([]byte(test.content))
			if test.wantErr == "" && err != nil {
				t.Fatalf("validateCompatibility() error = %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("validateCompatibility() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestIdentityAndPins(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module "+modulePath+"\n\ngo 1.26.0\n\ntoolchain go1.26.5\n")
	writeFile(t, root, "compatibility.json", `{"schema":1,"go":"1.26.5","spice":null,"spice_toolchain":null,"spice_agent":null,"spice_agent_tui":null,"spice_agent_provider_openai":null,"spice_agent_tools_coding":null}`)
	writeFile(t, root, "tools/go.mod", strings.Join([]string{
		"github.com/golangci/golangci-lint/v2 v2.12.2",
		"github.com/securego/gosec/v2 v2.28.0",
		"go.uber.org/nilaway v0.0.0-20260724203407-f4f8ac24c032",
		"golang.org/x/tools v0.48.0", "golang.org/x/vuln v1.1.4", "mvdan.cc/gofumpt v0.10.0",
	}, "\n"))
	if err := checkIdentity(root); err != nil {
		t.Fatalf("checkIdentity() error = %v", err)
	}
	writeFile(t, root, "go.mod", "module example.com/wrong\n")
	if err := checkIdentity(root); err == nil || !strings.Contains(err.Error(), "canonical module") {
		t.Fatalf("checkIdentity() error = %v, want identity diagnostic", err)
	}
}

func TestFilesCoverageAndModeBoundaries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "root.go", "package fixture")
	writeFile(t, root, "internal/value.go", "package value")
	writeFile(t, root, "tools/ignored.go", "package ignored")
	files, err := goFiles(root)
	if err != nil || len(files) != 2 || !slices.IsSorted(files) {
		t.Fatalf("goFiles() = %v, %v", files, err)
	}
	digests, err := treeDigests(root)
	if err != nil || len(digests) != 3 {
		t.Fatalf("treeDigests() = %d, %v", len(digests), err)
	}
	missing, err := treeDigests(filepath.Join(root, "missing"))
	if err != nil || len(missing) != 0 {
		t.Fatalf("treeDigests(missing) = %v, %v", missing, err)
	}
	percentage, err := totalCoverage("total: (statements) 90.0%")
	if err != nil || percentage != 90 {
		t.Fatalf("totalCoverage() = %v, %v", percentage, err)
	}
	if _, err := totalCoverage("invalid"); err == nil {
		t.Fatal("totalCoverage(invalid) error = nil")
	}
	if err := run(t.Context(), root, "unknown"); err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("run(unknown) error = %v", err)
	}
}

func TestEnvironmentAndCancellation(t *testing.T) {
	t.Parallel()
	offline := commandEnvironment(false, map[string]string{"GOFLAGS": "-mod=vendor"})
	if !slices.Contains(offline, "GOPROXY=off") || !slices.Contains(offline, "GOWORK=off") ||
		!slices.Contains(offline, "GOFLAGS=-mod=vendor") {
		t.Fatalf("offline environment is not isolated")
	}
	for _, entry := range offline {
		upper := strings.ToUpper(entry)
		if strings.Contains(upper, "TOKEN=") || strings.Contains(upper, "SECRET=") {
			t.Fatalf("credential-like environment was inherited: %s", entry)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := command(ctx, t.TempDir(), nil, "go", "version")
	if err == nil || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("command(cancelled) error = %v", err)
	}
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
