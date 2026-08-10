package terminal

import (
	"time"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

const (
	endpointPollInterval = 25 * time.Millisecond
	startupTimeout       = 10 * time.Second
	startupRetryInterval = 25 * time.Millisecond
	shutdownTimeout      = 10 * time.Second
)
