package daemon

import (
	"path/filepath"
	"slices"
	"time"

	pluginv1 "github.com/spice-framework/spice-agent/plugin/v1"
	"github.com/spice-framework/spice-agent/tool"
)

// @import { ConfigurationProperties } from "github.com/spice-framework/spice/annotation/core"

// RuntimePluginProperties is the complete explicit single-plugin configuration
// surface. Its zero value disables runtime plugins. Any setting other than the
// generated ID and timeout defaults opts in and requires a complete, validated
// executable contract.
//
// @ConfigurationProperties(prefix="agent.runtime-plugin")
type RuntimePluginProperties struct {
	Required           bool          `spice:"required,env=SPICE_AGENT_RUNTIME_PLUGIN_REQUIRED"`
	ID                 string        `spice:"id,default=runtime-tool,env=SPICE_AGENT_RUNTIME_PLUGIN_ID"`
	Path               string        `spice:"path,env=SPICE_AGENT_RUNTIME_PLUGIN_PATH"`
	SHA256             string        `spice:"sha256,env=SPICE_AGENT_RUNTIME_PLUGIN_SHA256"`
	ManifestName       string        `spice:"manifest-name,env=SPICE_AGENT_RUNTIME_PLUGIN_MANIFEST_NAME"`
	ManifestVersion    string        `spice:"manifest-version,env=SPICE_AGENT_RUNTIME_PLUGIN_MANIFEST_VERSION"`
	WorkingDirectory   string        `spice:"working-directory,env=SPICE_AGENT_RUNTIME_PLUGIN_WORKING_DIRECTORY"`
	FilesystemRead     bool          `spice:"capabilities.filesystem-read,env=SPICE_AGENT_RUNTIME_PLUGIN_FILESYSTEM_READ"`
	FilesystemWrite    bool          `spice:"capabilities.filesystem-write,env=SPICE_AGENT_RUNTIME_PLUGIN_FILESYSTEM_WRITE"`
	ProcessExecute     bool          `spice:"capabilities.process-execute,env=SPICE_AGENT_RUNTIME_PLUGIN_PROCESS_EXECUTE"`
	NetworkAccess      bool          `spice:"capabilities.network-access,env=SPICE_AGENT_RUNTIME_PLUGIN_NETWORK_ACCESS"`
	SecretsRead        bool          `spice:"capabilities.secrets-read,env=SPICE_AGENT_RUNTIME_PLUGIN_SECRETS_READ"`
	EnvironmentRead    bool          `spice:"capabilities.environment-read,env=SPICE_AGENT_RUNTIME_PLUGIN_ENVIRONMENT_READ"`
	EnvironmentWrite   bool          `spice:"capabilities.environment-write,env=SPICE_AGENT_RUNTIME_PLUGIN_ENVIRONMENT_WRITE"`
	StartupTimeout     time.Duration `spice:"timeouts.startup,default=10s,env=SPICE_AGENT_RUNTIME_PLUGIN_STARTUP_TIMEOUT"`
	CallTimeout        time.Duration `spice:"timeouts.call,default=2m,env=SPICE_AGENT_RUNTIME_PLUGIN_CALL_TIMEOUT"`
	DrainTimeout       time.Duration `spice:"timeouts.drain,default=10s,env=SPICE_AGENT_RUNTIME_PLUGIN_DRAIN_TIMEOUT"`
	ShutdownTimeout    time.Duration `spice:"timeouts.shutdown,default=10s,env=SPICE_AGENT_RUNTIME_PLUGIN_SHUTDOWN_TIMEOUT"`
	ContainmentTimeout time.Duration `spice:"timeouts.containment,default=5s,env=SPICE_AGENT_RUNTIME_PLUGIN_CONTAINMENT_TIMEOUT"`
}

func (properties RuntimePluginProperties) cleanupTimeout() time.Duration {
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

func (properties RuntimePluginProperties) disabled() bool {
	return !properties.Required && properties.Path == "" &&
		(properties.ID == "" || properties.ID == defaultRuntimePluginID) &&
		properties.SHA256 == "" && properties.ManifestName == "" &&
		properties.ManifestVersion == "" && properties.WorkingDirectory == "" &&
		properties.capabilitiesDisabled() && properties.timeoutsDefault()
}

func (properties RuntimePluginProperties) capabilitiesDisabled() bool {
	return !properties.FilesystemRead && !properties.FilesystemWrite &&
		!properties.ProcessExecute && !properties.NetworkAccess &&
		!properties.SecretsRead && !properties.EnvironmentRead && !properties.EnvironmentWrite
}

func (properties RuntimePluginProperties) timeoutsDefault() bool {
	return properties.defaultOrZeroDuration(
		properties.StartupTimeout, defaultRuntimePluginStartupTimeout,
	) && properties.defaultOrZeroDuration(properties.CallTimeout, defaultRuntimePluginCallTimeout) &&
		properties.defaultOrZeroDuration(properties.DrainTimeout, defaultRuntimePluginDrainTimeout) &&
		properties.defaultOrZeroDuration(properties.ShutdownTimeout, defaultRuntimePluginShutdownTimeout) &&
		properties.defaultOrZeroDuration(properties.ContainmentTimeout, defaultRuntimePluginContainmentTimeout)
}

func (RuntimePluginProperties) defaultOrZeroDuration(value, defaultValue time.Duration) bool {
	return value == 0 || value == defaultValue
}

func (RuntimePluginProperties) absoluteCanonicalPath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value
}

func (properties RuntimePluginProperties) capabilities() []tool.Capability {
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

func (RuntimePluginProperties) limits() *pluginv1.Limits {
	return &pluginv1.Limits{
		MaxMessageBytes: 1 << 20, MaxTools: 256, MaxSchemaBytes: 64 << 10,
		MaxCallArgumentBytes: 1 << 20, MaxResultBytes: 1 << 20,
		MaxProgressBytes: 4 << 10, MaxConcurrentCalls: 32,
	}
}
