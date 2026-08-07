package daemonprocess

import (
	"context"
	"testing"
	"time"
)

func TestRootExitBoundsInheritedPipeAndReapsDescendant(t *testing.T) {
	starter := helperStarter(t, "orphan", t.TempDir(), 1024)
	// Keep this regression tight independently of the race-runtime allowance
	// used by helpers that perform a graceful EOF shutdown.
	starter.terminate = 100 * time.Millisecond
	candidate, err := starter.Start(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process := requireProcess(t, candidate)
	waitHelperReady(t, process)

	wait, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err = candidate.Wait(wait)
	if err != nil || candidate.Result() != nil {
		t.Fatalf("inherited-pipe containment/result = %v/%v", err, candidate.Result())
	}
	child, found := childPID(process.ProtectedStderr())
	if !found {
		t.Fatalf("helper did not report descendant: %q", process.ProtectedStderr())
	}
	assertProcessStopped(t, child)
}

func TestAtomicLaunchContainsImmediateExitBeforeReaping(t *testing.T) {
	starter := helperStarter(t, "early", t.TempDir(), 256)
	for iteration := range 20 {
		candidate, err := starter.Start(t.Context())
		if err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		wait, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		err = candidate.Wait(wait)
		cancel()
		if err != nil || candidate.Result() == nil {
			t.Fatalf("iteration %d cleanup/result = %v/%v", iteration, err, candidate.Result())
		}
	}
}
