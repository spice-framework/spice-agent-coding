package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

type opencodeEnvironment struct {
	root          string
	home          string
	config        string
	configContent string
}

func (environment opencodeEnvironment) newOpenCodeEnvironment(root, config, configContent string) opencodeEnvironment {
	return opencodeEnvironment{
		root: filepath.Clean(root), home: filepath.Join(root, "home"), config: config, configContent: configContent,
	}
}

func (environment opencodeEnvironment) Values() []string {
	values := (opencodeEnvironment{}).minimumEvaluationEnvironment(environment.root)
	paths := map[string]string{
		"HOME":                    environment.home,
		"USERPROFILE":             environment.home,
		"XDG_CONFIG_HOME":         filepath.Join(environment.root, "config"),
		"XDG_DATA_HOME":           filepath.Join(environment.root, "data"),
		"XDG_CACHE_HOME":          filepath.Join(environment.root, "cache"),
		"XDG_STATE_HOME":          filepath.Join(environment.root, "state"),
		"APPDATA":                 filepath.Join(environment.root, "appdata"),
		"LOCALAPPDATA":            filepath.Join(environment.root, "localappdata"),
		"TMP":                     filepath.Join(environment.root, "tmp"),
		"TEMP":                    filepath.Join(environment.root, "tmp"),
		"TMPDIR":                  filepath.Join(environment.root, "tmp"),
		"OPENCODE_CONFIG":         environment.config,
		"OPENCODE_CONFIG_DIR":     filepath.Join(environment.root, "opencode-config"),
		"OPENCODE_CONFIG_CONTENT": environment.configContent,
		"OPENCODE_EXPERIMENTAL":   "false",
		"CI":                      "true",
	}
	for name, value := range paths {
		values = append(values, name+"="+value)
	}
	slices.Sort(values)
	return values
}

func (environment opencodeEnvironment) minimumEvaluationEnvironment(root string) []string {
	values := []string{
		"GOWORK=off",
		"GOTOOLCHAIN=local",
		"GOFLAGS=-mod=vendor",
		"GOPROXY=off",
		"GOSUMDB=off",
		"PATH=" + os.Getenv("PATH"),
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{"ComSpec", "PATHEXT", "SystemDrive", "SystemRoot", "WINDIR"} {
			if value := os.Getenv(name); value != "" && !strings.ContainsAny(value, "\r\n\x00") {
				values = append(values, name+"="+value)
			}
		}
	}
	if filepath.IsAbs(root) {
		values = append(values, "HOME="+filepath.Join(root, "home"))
	}
	slices.Sort(values)
	return values
}
