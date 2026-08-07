//go:build linux

package daemonprocess

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func processSnapshot() ([]processRecord, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	records := make([]processRecord, 0, len(entries))
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || pid <= 0 {
			continue
		}
		value, readErr := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if readErr != nil {
			// Process exit and procfs visibility races are normal while walking the
			// global table. Missing records cannot be treated as owned identities.
			if errors.Is(readErr, os.ErrNotExist) || errors.Is(readErr, os.ErrPermission) {
				continue
			}
			continue
		}
		record, valid := parseLinuxProcessStat(pid, string(value))
		if valid {
			records = append(records, record)
		}
	}
	return records, nil
}

func parseLinuxProcessStat(pid int, value string) (processRecord, bool) {
	closeName := strings.LastIndexByte(value, ')')
	if closeName < 0 || closeName+2 >= len(value) {
		return processRecord{}, false
	}
	fields := strings.Fields(value[closeName+2:])
	// fields[0] is state (field 3); ppid, pgrp, and starttime are fields
	// 4, 5, and 22 in proc_pid_stat(5).
	if len(fields) < 20 {
		return processRecord{}, false
	}
	ppid, ppidErr := strconv.Atoi(fields[1])
	pgid, pgidErr := strconv.Atoi(fields[2])
	started, startedErr := strconv.ParseUint(fields[19], 10, 64)
	if ppidErr != nil || pgidErr != nil || startedErr != nil || started == 0 {
		return processRecord{}, false
	}
	return processRecord{
		pid: pid, ppid: ppid, pgid: pgid,
		identity: processIdentity{startedPart: started},
	}, true
}
