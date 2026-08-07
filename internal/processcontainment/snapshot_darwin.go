//go:build darwin

package processcontainment

import "golang.org/x/sys/unix"

// Snapshot returns every visible identity-bearing process record.
func Snapshot() ([]Record, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(processes))
	for _, process := range processes {
		pid := int(process.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		records = append(records, Record{
			PID: pid, ParentID: int(process.Eproc.Ppid), GroupID: int(process.Eproc.Pgid),
			Identity: Identity{
				StartedSeconds: process.Proc.P_starttime.Sec,
				StartedPart:    uint64(process.Proc.P_starttime.Usec),
			},
		})
	}
	return records, nil
}
