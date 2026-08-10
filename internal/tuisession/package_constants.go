package tuisession

import "time"

const (
	defaultReplayLimit    = 256
	defaultUpdateCapacity = 64
	defaultReconnectDelay = 50 * time.Millisecond
	maximumUpdateCapacity = 1024
	maximumReconnectDelay = 5 * time.Second
)
