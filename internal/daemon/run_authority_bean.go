package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"context"

	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	"github.com/spice-framework/spice/lifecycle"
)

// NewRunAuthority opens the current-user persistent snapshot authority and
// registers its retained directory handle for generated reverse cleanup.
//
// @Bean(name="runAuthority")
func NewRunAuthority(
	properties Properties,
) (*agentdaemon.RunAuthority, lifecycle.Cleanup, error) {
	authority, err := agentdaemon.NewRunAuthority(agentdaemon.RunAuthorityConfig{
		Directory: properties.RunAuthorityDirectory,
	})
	if err != nil {
		return nil, nil, err
	}
	cleanup := func(context.Context) error { return authority.Close() }
	return authority, cleanup, nil
}
