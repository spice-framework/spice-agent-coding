package terminal

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

import (
	"github.com/spice-framework/spice-agent/client"
)

// NewInitializeRequest contributes protocol-1.3 replay-safe initialization.
//
// @Bean(name="terminalInitializeRequest")
func NewInitializeRequest(
	protocol client.ProtocolRange,
	build client.Build,
	limits client.Limits,
) (client.InitializeRequest, error) {
	attempt, err := client.NewInitializationAttemptID()
	if err != nil {
		return client.InitializeRequest{}, err
	}
	capabilities := []string{"events", "snapshot-authority-v1", "snapshots"}
	return client.NewInitializeRequestWithAttempt(
		protocol,
		build,
		capabilities,
		[]string{"events"},
		limits,
		attempt,
	)
}
