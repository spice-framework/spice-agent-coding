package daemon

type runtimeState uint8

const (
	runtimeNew runtimeState = iota
	runtimeStarting
	runtimeRunning
	runtimeStopping
	runtimeStopped
)
