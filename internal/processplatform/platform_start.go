package processplatform

import (
	"context"

	agentprocess "github.com/spice-framework/spice-agent/process"
)

type platformStart func(context.Context, agentprocess.Spec, ChildRegistrar) (agentprocess.Process, error)
