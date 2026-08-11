package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spice-framework/spice-agent-coding/internal/daemonprocess"
	workspaceconfig "github.com/spice-framework/spice-agent-coding/internal/workspace"
	coding "github.com/spice-framework/spice-agent-tools-coding"
)

// NewCodingConfig selects one canonical application-owned workspace for every
// compiled coding tool.
//
// @Bean(name="codingConfig")
// @Singleton
func NewCodingConfig(
	properties workspaceconfig.Properties,
	registry daemonprocess.RootRegistry,
) (coding.Config, error) {
	if registry == nil {
		return coding.Config{}, errors.New("daemon root registry is unavailable")
	}
	root, err := filepath.Abs(properties.Workspace)
	if err != nil {
		return coding.Config{}, fmt.Errorf("resolve coding workspace: %w", err)
	}
	return coding.Config{Root: filepath.Clean(root)}, nil
}
