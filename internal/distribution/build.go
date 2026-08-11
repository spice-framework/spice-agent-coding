package distribution

import (
	"fmt"
	"io"
)

const (
	// DaemonComponent is the stable engine-server component identity.
	DaemonComponent = "spice-agentd"
	// TerminalComponent is the stable interactive-client component identity.
	TerminalComponent = "spice-agent"
)

// Version and Commit intentionally remain variables so release builds can set
// one distribution-wide identity with Go linker -X flags. Development builds
// advertise honest, non-release values.
var (
	Version = "0.1.0-preview.1-dev"
	Commit  = "development"
)

// Build owns one immutable snapshot of the distribution identity.
type Build struct {
	version string
	commit  string
}

// NewBuild captures the linker-injected distribution identity.
func NewBuild() Build {
	return Build{version: Version, commit: Commit}
}

// WriteVersion writes the stable human-readable executable identity.
func (build Build) WriteVersion(writer io.Writer, component string) error {
	if writer == nil {
		return fmt.Errorf("version output is required")
	}
	if component != DaemonComponent && component != TerminalComponent {
		return fmt.Errorf("unsupported distribution component %q", component)
	}
	if build.version == "" || build.commit == "" {
		return fmt.Errorf("distribution identity is incomplete")
	}
	if _, err := fmt.Fprintf(writer, "%s %s (%s)\n", component, build.version, build.commit); err != nil {
		return fmt.Errorf("write distribution identity: %w", err)
	}
	return nil
}
