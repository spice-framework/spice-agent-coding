package daemonprocess

// launchedProcess is implemented independently on each supported platform.
// Close returns the joined history of every containment failure.
type launchedProcess interface {
	Wait() error
	CloseInput() error
	Terminate() error
	Kill() error
	Close() error
}
