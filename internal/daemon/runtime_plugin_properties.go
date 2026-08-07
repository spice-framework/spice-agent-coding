package daemon

import "time"

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
