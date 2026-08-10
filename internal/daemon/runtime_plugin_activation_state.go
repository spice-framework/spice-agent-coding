package daemon

type runtimePluginActivationState uint8

const (
	runtimePluginActivationNew runtimePluginActivationState = iota
	runtimePluginActivationStarting
	runtimePluginActivationReady
	runtimePluginActivationDegraded
	runtimePluginActivationFailed
)
