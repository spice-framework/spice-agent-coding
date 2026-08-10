package daemonprocess

import (
	"io"
	"time"
)

type processSpec struct {
	executable  string
	argument    string
	directory   string
	environment []string
	stderr      io.Writer
	waitDelay   time.Duration
}
