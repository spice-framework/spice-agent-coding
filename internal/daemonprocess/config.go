package daemonprocess

import (
	"strings"
	"time"
)

// Config controls only non-secret process mechanics. Environment is passed
// byte-for-byte to the child but is never formatted or included in an error.
type Config struct {
	Directory       string
	Environment     []string
	StderrBytes     int
	GracefulTimeout time.Duration
	TerminateDelay  time.Duration
}

func (config Config) validBounds() bool {
	return config.StderrBytes > 0 && config.StderrBytes <= 1<<20 &&
		config.GracefulTimeout > 0 && config.TerminateDelay > 0
}

func (Config) validEnvironment(environment []string) bool {
	for _, value := range environment {
		if strings.IndexByte(value, 0) >= 0 {
			return false
		}
	}
	return true
}
