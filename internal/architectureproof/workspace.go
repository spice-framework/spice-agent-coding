package architectureproof

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	coding "github.com/spice-framework/spice-agent-tools-coding"
	"github.com/spice-framework/spice/lifecycle"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

const fixtureDocument = "Spice Agent architecture proof\n"

// NewCodingConfig creates an isolated worktree owned by the generated
// application. Its cleanup proves that starter beans share the application's
// ordinary reverse-order lifecycle.
//
// @Bean(name="codingConfig")
func NewCodingConfig() (coding.Config, lifecycle.Cleanup, error) {
	root, err := os.MkdirTemp("", "spice-agent-architecture-proof-")
	if err != nil {
		return coding.Config{}, nil, fmt.Errorf("create architecture-proof worktree: %w", err)
	}
	cleanup := func(context.Context) error {
		if !strings.HasPrefix(filepath.Base(root), "spice-agent-architecture-proof-") {
			return fmt.Errorf("refuse to remove unexpected architecture-proof path %q", root)
		}
		return os.RemoveAll(root)
	}
	if err = os.WriteFile(filepath.Join(root, "README.md"), []byte(fixtureDocument), 0o600); err != nil {
		return coding.Config{}, nil, fmt.Errorf(
			"write architecture-proof fixture: %w",
			errors.Join(err, cleanup(context.Background())),
		)
	}
	return coding.Config{Root: root}, cleanup, nil
}
