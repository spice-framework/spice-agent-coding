package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/client"
	agentdaemon "github.com/spice-framework/spice-agent/daemon"
	"github.com/spice-framework/spice-agent/daemon/endpoint"
	"github.com/spice-framework/spice-agent/daemon/grpcserver"
)

// NewGRPCServer constructs the authenticated protocol boundary without
// opening or publishing a listener.
//
// @Bean(name="grpcServer")
func NewGRPCServer(
	root *Root,
	token endpoint.Token,
	host *agentdaemon.RunHost,
	sessions *agentdaemon.SessionStore,
	build client.Build,
) (*grpcserver.Server, error) {
	ctx, err := rootContext(root)
	if err != nil {
		return nil, err
	}
	return grpcserver.NewServer(grpcserver.ServerConfig{
		Root: ctx, EndpointToken: token, Host: host, Sessions: sessions,
		Build:           build,
		Capabilities:    []string{"events", "snapshot-authority-v1", "snapshots"},
		MaximumSessions: 1024,
	})
}
