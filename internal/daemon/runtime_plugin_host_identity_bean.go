package daemon

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"fmt"

	"github.com/spice-framework/spice-agent/client"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
)

// NewRuntimePluginHostIdentity adapts the daemon's single validated build
// provenance value to the Protobuf identity used only at the runtime-plugin
// process boundary. It neither discovers nor launches plugins.
//
// @Bean(name="runtimePluginHostIdentity")
func NewRuntimePluginHostIdentity(build client.Build) (*pluginv1.BuildIdentity, error) {
	if err := build.Validate(); err != nil {
		return nil, fmt.Errorf("construct runtime plugin host identity: %w", err)
	}
	identity := &pluginv1.BuildIdentity{
		Component: build.Component(),
		Version:   build.Version(),
		Commit:    build.Commit(),
		Runtime:   build.GoVersion(),
	}
	if err := pluginv1.ValidateBuildIdentity(identity); err != nil {
		return nil, fmt.Errorf("validate runtime plugin host identity: %w", err)
	}
	return identity, nil
}
