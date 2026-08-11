package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	agentdaemon "github.com/spice-framework/spice-agent/daemon"
)

// NewSessionStore constructs bounded client ownership rooted in the daemon
// lifetime.
//
// @Bean(name="sessionStore")
// @Singleton
func NewSessionStore(root *Root) (*agentdaemon.SessionStore, error) {
	ctx, err := root.Context()
	if err != nil {
		return nil, err
	}
	return agentdaemon.NewSessionStore(ctx, 1024)
}
