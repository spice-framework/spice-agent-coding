//go:build linux

package processcontainment

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Snapshot returns every visible identity-bearing process record.
func Snapshot() ([]Record, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || pid <= 0 {
			continue
		}
		value, readErr := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if readErr != nil {
			// Exit and procfs visibility races are normal while walking the
			// global table. Missing records are never treated as owned.
			if errors.Is(readErr, os.ErrNotExist) || errors.Is(readErr, os.ErrPermission) {
				continue
			}
			continue
		}
		record, valid := ParseLinuxProcessStat(pid, string(value))
		if valid {
			records = append(records, record)
		}
	}
	return records, nil
}

// ParseLinuxProcessStat decodes the identity fields used by Snapshot.
func ParseLinuxProcessStat(pid int, value string) (Record, bool) {
	closeName := strings.LastIndexByte(value, ')')
	if closeName < 0 || closeName+2 >= len(value) {
		return Record{}, false
	}
	fields := strings.Fields(value[closeName+2:])
	// fields[0] is state (field 3); ppid, pgrp, and starttime are fields
	// 4, 5, and 22 in proc_pid_stat(5).
	if len(fields) < 20 {
		return Record{}, false
	}
	parentID, parentErr := strconv.Atoi(fields[1])
	groupID, groupErr := strconv.Atoi(fields[2])
	started, startedErr := strconv.ParseUint(fields[19], 10, 64)
	if parentErr != nil || groupErr != nil || startedErr != nil || started == 0 {
		return Record{}, false
	}
	return Record{
		PID: pid, ParentID: parentID, GroupID: groupID,
		Identity: Identity{StartedPart: started},
	}, true
}
