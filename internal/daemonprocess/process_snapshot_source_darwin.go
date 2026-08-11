//go:build darwin

package daemonprocess

import "github.com/spice-framework/spice-agent-coding/internal/processcontainment"

type processSnapshotSource struct{}

func (processSnapshotSource) snapshot() ([]processRecord, error) {
	processes, err := processcontainment.NewSnapshotter().Snapshot()
	if err != nil {
		return nil, err
	}
	records := make([]processRecord, 0, len(processes))
	for _, process := range processes {
		records = append(records, processRecord{
			pid: process.PID, ppid: process.ParentID, pgid: process.GroupID,
			identity: processIdentity{
				startedSeconds: process.Identity.StartedSeconds,
				startedPart:    process.Identity.StartedPart,
			},
		})
	}
	return records, nil
}
