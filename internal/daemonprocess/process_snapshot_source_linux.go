//go:build linux

package daemonprocess

import "github.com/spice-framework/spice-agent-coding/internal/processcontainment"

type processSnapshotSource struct{}

func (processSnapshotSource) snapshot() ([]processRecord, error) {
	snapshot, err := processcontainment.Snapshot()
	if err != nil {
		return nil, err
	}
	records := make([]processRecord, 0, len(snapshot))
	for _, record := range snapshot {
		records = append(records, (processSnapshotSource{}).localRecord(record))
	}
	return records, nil
}

func (processSnapshotSource) parseLinuxStat(pid int, value string) (processRecord, bool) {
	record, valid := processcontainment.ParseLinuxProcessStat(pid, value)
	if !valid {
		return processRecord{}, false
	}
	return (processSnapshotSource{}).localRecord(record), true
}

func (processSnapshotSource) localRecord(record processcontainment.Record) processRecord {
	return processRecord{
		pid: record.PID, ppid: record.ParentID, pgid: record.GroupID,
		identity: processIdentity{
			startedSeconds: record.Identity.StartedSeconds,
			startedPart:    record.Identity.StartedPart,
		},
	}
}
