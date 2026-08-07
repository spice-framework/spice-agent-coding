//go:build darwin

package daemonprocess

import "golang.org/x/sys/unix"

func processSnapshot() ([]processRecord, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	records := make([]processRecord, 0, len(processes))
	for _, process := range processes {
		pid := int(process.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		records = append(records, processRecord{
			pid: pid, ppid: int(process.Eproc.Ppid), pgid: int(process.Eproc.Pgid),
			identity: processIdentity{
				startedSeconds: process.Proc.P_starttime.Sec,
				startedPart:    uint64(process.Proc.P_starttime.Usec),
			},
		})
	}
	return records, nil
}
