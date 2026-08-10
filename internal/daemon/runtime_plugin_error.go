package daemon

type runtimePluginError string

const (
	// ErrRuntimePluginRequiredUnavailable is the fixed, secret-safe startup
	// failure returned when a required configured plugin cannot be activated.
	ErrRuntimePluginRequiredUnavailable runtimePluginError = "required runtime plugin is unavailable"
	// ErrRuntimePluginActivationPending prevents transport publication before
	// the generated activation lifecycle has reached a terminal admission state.
	ErrRuntimePluginActivationPending runtimePluginError = "runtime plugin activation is incomplete"
)

func (problem runtimePluginError) Error() string { return string(problem) }
