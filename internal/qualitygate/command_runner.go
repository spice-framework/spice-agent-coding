package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// commandRunner owns exact executable resolution and closed command environments.
type commandRunner struct{}

func (owner commandRunner) toolPath(ctx context.Context, root, name string) (string, error) {
	content, err := (commandRunner{}).capture(ctx, root, "go", "tool", "-C", "tools", "-n", name)
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(content)
	if path == "" {
		return "", fmt.Errorf("resolve tool %q: empty path", name)
	}
	return path, nil
}

func (owner commandRunner) repositoryRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		content, readErr := os.ReadFile(filepath.Join(current, "go.mod")) // #nosec G304 -- bounded ancestor search.
		if readErr == nil && bytes.Contains(content, []byte("module "+modulePath+"\n")) {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("find repository root: go.mod not found")
		}
		current = parent
	}
}

func (owner commandRunner) command(ctx context.Context, directory string, overrides map[string]string, executable string, arguments ...string) error {
	executable = (commandRunner{}).qualityExecutable(executable)
	// #nosec G204,G702 -- executable paths are gate-owned and arguments are discrete.
	cmd := exec.CommandContext(ctx, executable, arguments...)
	cmd.Dir = directory
	cmd.Env = (commandRunner{}).commandEnvironment(false, overrides)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", executable, strings.Join(arguments, " "), err)
	}
	return nil
}

func (owner commandRunner) networkCommand(ctx context.Context, directory string, arguments ...string) error {
	// #nosec G204,G702 -- fixed Go executable and gate-owned discrete arguments.
	cmd := exec.CommandContext(ctx, (commandRunner{}).exactGoExecutable(), arguments...)
	cmd.Dir = directory
	cmd.Env = (commandRunner{}).commandEnvironment(true, nil)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go %s: %w", strings.Join(arguments, " "), err)
	}
	return nil
}

func (owner commandRunner) capture(ctx context.Context, directory, executable string, arguments ...string) (string, error) {
	executable = (commandRunner{}).qualityExecutable(executable)
	// #nosec G204,G702 -- executable paths are gate-owned and arguments are discrete.
	cmd := exec.CommandContext(ctx, executable, arguments...)
	cmd.Dir = directory
	cmd.Env = (commandRunner{}).commandEnvironment(false, nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", executable, strings.Join(arguments, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (owner commandRunner) qualityExecutable(executable string) string {
	if executable == "go" {
		return (commandRunner{}).exactGoExecutable()
	}
	return executable
}

func (owner commandRunner) exactGoExecutable() string {
	return filepath.Join(runtime.GOROOT(), "bin", (commandRunner{}).goExecutableName(runtime.GOOS)) //nolint:staticcheck // Gate runs in place under the selected exact toolchain.
}

func (owner commandRunner) goExecutableName(goos string) string {
	if goos == "windows" {
		return "go.exe"
	}
	return "go"
}

func (owner commandRunner) commandEnvironment(network bool, overrides map[string]string) []string {
	values := map[string]string{"GOFLAGS": "", "GOTOOLCHAIN": "local", "GOWORK": "off"}
	if network {
		values["GOAUTH"] = "off"
		values["GONOPROXY"] = ""
		values["GONOSUMDB"] = ""
		values["GOPRIVATE"] = ""
		values["GOPROXY"] = "https://proxy.golang.org"
		values["GOSUMDB"] = "sum.golang.org"
	} else {
		values["GOPROXY"] = "off"
	}
	maps.Copy(values, overrides)
	result := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		upperName := strings.ToUpper(name)
		if strings.Contains(upperName, "TOKEN") || strings.Contains(upperName, "SECRET") ||
			strings.Contains(upperName, "PASSWORD") || strings.HasSuffix(upperName, "_KEY") {
			continue
		}
		if _, replaced := values[upperName]; !replaced {
			result = append(result, entry)
		}
	}
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	slices.Sort(result)
	return result
}

func (owner commandRunner) removeTree(path string) {
	if err := os.RemoveAll(path); err != nil {
		fmt.Fprintf(os.Stdout, "warning: remove temporary tree %q: %v\n", path, err) //nolint:errcheck // Best-effort cleanup warning.
	}
}
