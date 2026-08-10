//go:build linux || darwin

package processplatform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spice-framework/spice-agent-coding/internal/testpath"

	"github.com/spice-framework/spice-agent-coding/internal/processcontainment"
	agentprocess "github.com/spice-framework/spice-agent/process"
)

func TestUnixIdentityDiscoveryRejectsReusedProcesses(t *testing.T) {
	t.Parallel()
	rootIdentity := processcontainment.Identity{StartedPart: 10}
	childIdentity := processcontainment.Identity{StartedPart: 11}
	owned := &unixProcess{
		groupID: 100, root: rootIdentity,
		children: map[int]processcontainment.Identity{101: childIdentity},
	}
	owned.discoverLocked(map[int]processcontainment.Record{
		100: {PID: 100, ParentID: 1, GroupID: 100, Identity: processcontainment.Identity{StartedPart: 99}},
		101: {PID: 101, ParentID: 100, GroupID: 100, Identity: processcontainment.Identity{StartedPart: 98}},
	})
	if len(owned.children) != 0 {
		t.Fatalf("reused identities remained owned: %#v", owned.children)
	}

	owned.root = rootIdentity
	owned.discoverLocked(map[int]processcontainment.Record{
		100: {PID: 100, ParentID: 1, GroupID: 100, Identity: rootIdentity},
		102: {PID: 102, ParentID: 100, GroupID: 100, Identity: processcontainment.Identity{StartedPart: 12}},
		103: {PID: 103, ParentID: 102, GroupID: 200, Identity: processcontainment.Identity{StartedPart: 13}},
	})
	if owned.children[102].StartedPart != 12 || owned.children[103].StartedPart != 13 {
		t.Fatalf("descendant closure = %#v", owned.children)
	}
}

func TestUnixProcessBoundaryFormattingAndNilSafety(t *testing.T) {
	t.Parallel()
	var owned *unixProcess
	if owned.Done() != nil {
		t.Fatal("nil process exposed Done")
	}
	if _, err := owned.Result(); err == nil {
		t.Fatal("nil process exposed Result")
	}
	if err := owned.RequestStop(t.Context()); err == nil {
		t.Fatal("nil process stop succeeded")
	}
	if err := owned.ForceKill(t.Context()); err == nil {
		t.Fatal("nil process kill succeeded")
	}
	if err := owned.Wait(t.Context()); err == nil {
		t.Fatal("nil process wait succeeded")
	}
	owned = &unixProcess{}
	for _, rendered := range []string{
		fmt.Sprint(owned), fmt.Sprintf("%#v", owned), fmt.Sprintf("%+v", owned), owned.LogValue().String(),
	} {
		if rendered != "processplatform.Process([REDACTED])" {
			t.Fatalf("process formatting = %q", rendered)
		}
	}
	process := &unixProcess{}
	if !process.channelClosed(closedProcessChannel()) || process.channelClosed(make(chan struct{})) {
		t.Fatal("channel completion classification changed")
	}
	if result := process.deriveOutcome(nil, nil); result.resultErr == nil || result.cleanupErr != nil ||
		strings.Contains(result.resultErr.Error(), "root process") {
		t.Fatalf("missing root outcome = %v, %v", result.resultErr, result.cleanupErr)
	}
}

func TestUnixAnchorFailureAbortsRootAndReturnsTerminalWaitFailure(t *testing.T) {
	t.Parallel()

	root := testpath.TempDir(t)
	executable := installProcessHelper(t, root, "anchor-failure")
	spec := helperSpec(t, executable, root, "blocked", strings.NewReader(""), io.Discard, io.Discard, nil)
	snapshotFailure := errors.New("private process snapshot failure")
	candidate, startErr := (&unixProcess{}).startWithSnapshot(
		spec,
		noopRegistrar{},
		func() ([]processcontainment.Record, error) { return nil, snapshotFailure },
	)
	if candidate == nil || !errors.Is(startErr, snapshotFailure) {
		t.Fatalf("anchor failure launch = %T, %v", candidate, startErr)
	}
	owned, ok := candidate.(*unixProcess)
	if !ok {
		t.Fatalf("anchor failure candidate = %T", candidate)
	}

	joined, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	waitErr := owned.Wait(joined)
	if waitErr == nil || !errors.Is(waitErr, snapshotFailure) {
		t.Fatalf("anchor failure wait = %v", waitErr)
	}
	var classified interface{ Retryable() bool }
	if !errors.As(waitErr, &classified) || classified.Retryable() {
		t.Fatalf("anchor failure retry classification = %T, %v", waitErr, waitErr)
	}
	outcome, resultErr := owned.Result()
	if resultErr != nil || outcome.Kind() != agentprocess.OutcomeSignaled {
		t.Fatalf("aborted root outcome = %#v, %v", outcome, resultErr)
	}
}
