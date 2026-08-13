package devacceptance

import (
	"testing"
	"time"
)

func waitForDevelopmentApplicationIdentity(
	t *testing.T,
	parentPID int,
	output *eventBuffer,
) developmentApplicationIdentity {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		identities, err := directDevelopmentApplicationIdentities(parentPID)
		if err == nil && len(identities) == 1 {
			return identities[0]
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf(
				"timed out waiting for one application child of supervisor %d: identities=%v error=%v\n%s",
				parentPID, identities, err, diagnosticTail(output.String()),
			)
		}
	}
}
