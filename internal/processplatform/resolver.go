// Package processplatform provides the distribution-owned operating-system
// adapters for the provider-neutral process contracts.
package processplatform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	agentprocess "github.com/spice-framework/spice-agent/process"
)

var errExecutableUnavailable = errors.New("requested executable is unavailable")

// Resolver resolves executables from only the immutable lookup value supplied
// by its caller. It never consults ambient environment state or a shell.
type Resolver struct{}

// NewResolver constructs the stateless platform resolver.
func NewResolver() *Resolver { return &Resolver{} }

// Resolve returns an existing canonical absolute executable path.
func (*Resolver) Resolve(ctx context.Context, lookup agentprocess.Lookup) (string, error) {
	if ctx == nil {
		return "", agentprocess.NewFailure(agentprocess.OperationResolve, errors.New("resolve context is required"))
	}
	if cause := context.Cause(ctx); cause != nil {
		return "", agentprocess.NewFailure(agentprocess.OperationResolve, cause)
	}
	if err := lookup.Validate(); err != nil {
		return "", agentprocess.NewFailure(agentprocess.OperationResolve, err)
	}

	requested := lookup.RequestedExecutable()
	directory := lookup.WorkingDirectory()
	var bases []string
	switch {
	case filepath.IsAbs(requested):
		bases = []string{filepath.Clean(requested)}
	case containsPathSeparator(requested):
		bases = []string{filepath.Clean(filepath.Join(directory, requested))}
	default:
		pathValue, found := environmentValue(lookup.Environment(), "PATH")
		if !found {
			return "", agentprocess.NewFailure(agentprocess.OperationResolve, errExecutableUnavailable)
		}
		for _, entry := range filepath.SplitList(pathValue) {
			if entry == "" {
				entry = directory
			} else if !filepath.IsAbs(entry) {
				entry = filepath.Join(directory, entry)
			}
			bases = append(bases, filepath.Clean(filepath.Join(entry, requested)))
		}
	}

	extensions := executableExtensions(requested, lookup.Environment())
	for _, base := range bases {
		for _, extension := range extensions {
			if cause := context.Cause(ctx); cause != nil {
				return "", agentprocess.NewFailure(agentprocess.OperationResolve, cause)
			}
			resolved, err := canonicalExecutable(base + extension)
			if err == nil {
				return resolved, nil
			}
		}
	}
	return "", agentprocess.NewFailure(agentprocess.OperationResolve, errExecutableUnavailable)
}

func canonicalExecutable(candidate string) (string, error) {
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	realPath, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	realPath, err = filepath.Abs(realPath)
	if err != nil {
		return "", err
	}
	realPath = filepath.Clean(realPath)
	info, err := os.Stat(realPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || !platformExecutable(info) {
		return "", errExecutableUnavailable
	}
	return realPath, nil
}

func containsPathSeparator(value string) bool {
	return strings.ContainsRune(value, filepath.Separator) ||
		(filepath.Separator != '/' && strings.ContainsRune(value, '/'))
}

func environmentValue(environment []string, name string) (string, bool) {
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if found && environmentNameEqual(key, name) {
			return value, true
		}
	}
	return "", false
}

var _ agentprocess.ExecutableResolver = (*Resolver)(nil)
