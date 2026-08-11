package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

import (
	"errors"
	"path/filepath"

	pluginhost "github.com/spice-framework/spice-agent/plugin/host"
)

// NewRuntimePluginPlan validates the complete configuration without opening or
// launching its executable. Returned errors identify only fixed field classes
// and never include a configured path, digest, endpoint, or manifest value.
//
// @Bean(name="runtimePluginPlan")
// @Singleton
func NewRuntimePluginPlan(properties RuntimePluginProperties) (RuntimePluginPlan, error) {
	cleanupTimeout := properties.cleanupTimeout()
	if properties.disabled() {
		set, err := pluginhost.NewSet(nil)
		if err != nil {
			return RuntimePluginPlan{}, errors.New("construct disabled runtime plugin plan")
		}
		return RuntimePluginPlan{cleanupTimeout: cleanupTimeout, set: set}, nil
	}
	if properties.ID == "" || properties.Path == "" || properties.SHA256 == "" ||
		properties.ManifestName == "" || properties.ManifestVersion == "" {
		return RuntimePluginPlan{}, errors.New("runtime plugin configuration is partial")
	}
	if !properties.absoluteCanonicalPath(properties.Path) {
		return RuntimePluginPlan{}, errors.New("runtime plugin executable path must be absolute and canonical")
	}
	workingDirectory := properties.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = filepath.Dir(properties.Path)
	}
	if !properties.absoluteCanonicalPath(workingDirectory) {
		return RuntimePluginPlan{}, errors.New("runtime plugin working directory must be absolute and canonical")
	}
	digest, err := pluginhost.ParseSHA256(properties.SHA256)
	if err != nil {
		return RuntimePluginPlan{}, errors.New("runtime plugin SHA-256 must be canonical lowercase hexadecimal")
	}
	executable, err := pluginhost.NewExecutable(pluginhost.ExecutableConfig{
		ID: properties.ID, ManifestName: properties.ManifestName,
		ManifestVersion: properties.ManifestVersion, Path: properties.Path,
		SHA256: digest, WorkingDirectory: workingDirectory,
		Environment: []string{}, ApprovedCapabilities: properties.capabilities(),
		RequestedLimits: properties.limits(), StartupTimeout: properties.StartupTimeout,
		CallTimeout: properties.CallTimeout, DrainTimeout: properties.DrainTimeout,
		ShutdownTimeout:    properties.ShutdownTimeout,
		ContainmentTimeout: properties.ContainmentTimeout,
	})
	if err != nil {
		return RuntimePluginPlan{}, errors.New("runtime plugin executable configuration is invalid")
	}
	set, err := pluginhost.NewSet([]pluginhost.Executable{executable})
	if err != nil {
		return RuntimePluginPlan{}, errors.New("runtime plugin executable set is invalid")
	}
	return RuntimePluginPlan{
		enabled: true, required: properties.Required,
		cleanupTimeout: cleanupTimeout, set: set,
	}, nil
}
