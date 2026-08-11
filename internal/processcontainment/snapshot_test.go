//go:build linux || darwin

package processcontainment

import (
	"os"
	"testing"
)

func TestSnapshotContainsCurrentImmutableIdentity(t *testing.T) {
	t.Parallel()
	records, err := NewSnapshotter().Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.PID != os.Getpid() {
			continue
		}
		if record.ParentID <= 0 || record.GroupID <= 0 || record.Identity.IsZero() {
			t.Fatalf("current process record = %#v", record)
		}
		return
	}
	t.Fatal("current process is absent from its own process-table snapshot")
}

func TestZeroIdentity(t *testing.T) {
	t.Parallel()
	if !(Identity{}).IsZero() || (Identity{StartedPart: 1}).IsZero() ||
		(Identity{StartedSeconds: 1}).IsZero() {
		t.Fatal("identity zero classification changed")
	}
}
