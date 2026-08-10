package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"time"

	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"github.com/spice-framework/spice-agent/tool"
)

const (
	defaultRuntimePluginID                 = "runtime-tool"
	defaultRuntimePluginStartupTimeout     = 10 * time.Second
	defaultRuntimePluginCallTimeout        = 2 * time.Minute
	defaultRuntimePluginDrainTimeout       = 10 * time.Second
	defaultRuntimePluginShutdownTimeout    = 10 * time.Second
	defaultRuntimePluginContainmentTimeout = 5 * time.Second
	runtimePluginCleanupGrace              = time.Second
	maximumDuration                        = time.Duration(1<<63 - 1)
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// RuntimePluginPlan is an immutable, validated complete desired configuration.
// It contains either no executable or exactly one explicitly configured
// executable; it performs no discovery or filesystem access.
type RuntimePluginPlan struct {
	enabled        bool
	required       bool
	cleanupTimeout time.Duration
	set            pluginhost.Set
}

// Enabled reports whether the application explicitly configured a plugin.
func (plan RuntimePluginPlan) Enabled() bool { return plan.enabled }

// Required reports whether activation failure must prevent endpoint publication.
func (plan RuntimePluginPlan) Required() bool { return plan.required }

// Set returns the immutable complete desired set. The host API exposes only
// independently backed executable snapshots from that value.
func (plan RuntimePluginPlan) Set() pluginhost.Set {
	return plan.set
}

// String intentionally exposes only bounded admission state. In particular,
// formatting a plan must never disclose its executable path or digest.
func (plan RuntimePluginPlan) String() string {
	return fmt.Sprintf(
		"RuntimePluginPlan{enabled:%t required:%t plugins:%d}",
		plan.enabled,
		plan.required,
		plan.set.Len(),
	)
}

// GoString preserves the same redaction contract for diagnostic %#v output.
func (plan RuntimePluginPlan) GoString() string { return plan.String() }

// Format preserves redaction for every fmt formatting verb and flag.
func (plan RuntimePluginPlan) Format(state fmt.State, _ rune) {
	if _, err := io.WriteString(state, plan.String()); err != nil {
		return
	}
}

// MarshalJSON exposes bounded state rather than private executable metadata.
func (plan RuntimePluginPlan) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Enabled  bool `json:"enabled"`
		Required bool `json:"required"`
		Plugins  int  `json:"plugins"`
	}{
		Enabled:  plan.enabled,
		Required: plan.required,
		Plugins:  plan.set.Len(),
	})
}

// Validate rejects zero/corrupted enabled plans without performing I/O.
func (plan RuntimePluginPlan) Validate() error {
	if err := plan.set.Validate(); err != nil {
		return errors.New("runtime plugin plan set is invalid")
	}
	if plan.enabled != (plan.set.Len() == 1) || plan.set.Len() > 1 || plan.required && !plan.enabled ||
		plan.cleanupTimeout <= 0 {
		return errors.New("runtime plugin plan state is invalid")
	}
	return nil
}

func runtimePluginCleanupTimeout(properties RuntimePluginProperties) time.Duration {
	total := runtimePluginCleanupGrace
	for _, candidate := range []struct {
		value        time.Duration
		defaultValue time.Duration
	}{
		{properties.DrainTimeout, defaultRuntimePluginDrainTimeout},
		{properties.ShutdownTimeout, defaultRuntimePluginShutdownTimeout},
		{properties.ContainmentTimeout, defaultRuntimePluginContainmentTimeout},
	} {
		value := candidate.value
		if value == 0 {
			value = candidate.defaultValue
		}
		if value > maximumDuration-total {
			return maximumDuration
		}
		total += value
	}
	return total
}

func runtimePluginPropertiesDisabled(properties RuntimePluginProperties) bool {
	return !properties.Required && properties.Path == "" &&
		(properties.ID == "" || properties.ID == defaultRuntimePluginID) &&
		properties.SHA256 == "" && properties.ManifestName == "" &&
		properties.ManifestVersion == "" && properties.WorkingDirectory == "" &&
		runtimePluginCapabilitiesDisabled(properties) && runtimePluginTimeoutsDefault(properties)
}

func runtimePluginCapabilitiesDisabled(properties RuntimePluginProperties) bool {
	return !properties.FilesystemRead && !properties.FilesystemWrite &&
		!properties.ProcessExecute && !properties.NetworkAccess &&
		!properties.SecretsRead && !properties.EnvironmentRead && !properties.EnvironmentWrite
}

func runtimePluginTimeoutsDefault(properties RuntimePluginProperties) bool {
	return defaultOrZeroDuration(
		properties.StartupTimeout, defaultRuntimePluginStartupTimeout,
	) && defaultOrZeroDuration(properties.CallTimeout, defaultRuntimePluginCallTimeout) &&
		defaultOrZeroDuration(properties.DrainTimeout, defaultRuntimePluginDrainTimeout) &&
		defaultOrZeroDuration(properties.ShutdownTimeout, defaultRuntimePluginShutdownTimeout) &&
		defaultOrZeroDuration(properties.ContainmentTimeout, defaultRuntimePluginContainmentTimeout)
}

func defaultOrZeroDuration(value, defaultValue time.Duration) bool {
	return value == 0 || value == defaultValue
}

func absoluteCanonicalPath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value
}

func runtimePluginCapabilities(properties RuntimePluginProperties) []tool.Capability {
	values := make([]tool.Capability, 0, 7)
	// This lexical order is part of the distribution contract. The process
	// execute capability used to launch the plugin itself remains host-owned;
	// this list approves only capabilities declared by plugin tools.
	for _, candidate := range []struct {
		enabled    bool
		capability tool.Capability
	}{
		{properties.EnvironmentRead, tool.CapabilityEnvironmentRead},
		{properties.EnvironmentWrite, tool.CapabilityEnvironmentWrite},
		{properties.FilesystemRead, tool.CapabilityFilesystemRead},
		{properties.FilesystemWrite, tool.CapabilityFilesystemWrite},
		{properties.NetworkAccess, tool.CapabilityNetworkAccess},
		{properties.ProcessExecute, tool.CapabilityProcessExecute},
		{properties.SecretsRead, tool.CapabilitySecretsRead},
	} {
		if candidate.enabled {
			values = append(values, candidate.capability)
		}
	}
	return slices.Clip(values)
}

func runtimePluginLimits() *pluginv1.Limits {
	return &pluginv1.Limits{
		MaxMessageBytes: 1 << 20, MaxTools: 256, MaxSchemaBytes: 64 << 10,
		MaxCallArgumentBytes: 1 << 20, MaxResultBytes: 1 << 20,
		MaxProgressBytes: 4 << 10, MaxConcurrentCalls: 32,
	}
}
