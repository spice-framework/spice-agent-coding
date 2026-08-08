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

// WriteVersion writes the stable human-readable executable identity.
func WriteVersion(writer io.Writer, component string) error {
	if writer == nil {
		return fmt.Errorf("version output is required")
	}
	if component != DaemonComponent && component != TerminalComponent {
		return fmt.Errorf("unsupported distribution component %q", component)
	}
	if Version == "" || Commit == "" {
		return fmt.Errorf("distribution identity is incomplete")
	}
	if _, err := fmt.Fprintf(writer, "%s %s (%s)\n", component, Version, Commit); err != nil {
		return fmt.Errorf("write distribution identity: %w", err)
	}
	return nil
}
