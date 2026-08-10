package terminal

import "time"

const (
	endpointPollInterval = 25 * time.Millisecond
	startupTimeout       = 10 * time.Second
	startupRetryInterval = 25 * time.Millisecond
	shutdownTimeout      = 10 * time.Second
)
