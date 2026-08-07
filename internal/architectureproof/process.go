package architectureproof

import (
	"errors"
	"os"

	"github.com/spice-framework/spice-agent-coding/internal/processplatform"
	agentprocess "github.com/spice-framework/spice-agent/process"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// proofChildRegistrar is deliberately inert because the embedded proof has no
// parent daemon supervisor. The returned launcher still owns each child through
// its native per-process Job or identity-tracked process-group boundary.
type proofChildRegistrar struct{}

func (proofChildRegistrar) Register(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return errors.New("architecture-proof process is invalid")
	}
	return nil
}

// NewExecutableResolver contributes the same native resolver used by the
// distribution daemon.
//
// @Bean(name="processResolver")
func NewExecutableResolver() agentprocess.ExecutableResolver {
	return processplatform.NewResolver()
}

// NewProcessLauncher contributes a self-owned launcher for the embedded proof.
//
// @Bean(name="processLauncher")
func NewProcessLauncher() (agentprocess.Launcher, error) {
	return processplatform.NewLauncher(proofChildRegistrar{})
}
