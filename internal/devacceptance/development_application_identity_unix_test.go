//go:build !windows

package devacceptance

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type developmentApplicationIdentity struct {
	PID     int
	Created string
}

func directDevelopmentApplicationIdentities(parentPID int) ([]developmentApplicationIdentity, error) {
	command := exec.Command("ps", "-axo", "pid=,ppid=,lstart=,comm=")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list development processes: %w", err)
	}
	var result []developmentApplicationIdentity
	for line := range strings.SplitSeq(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if pidErr != nil || parentErr != nil || parent != parentPID ||
			filepath.Base(fields[7]) != "application" {
			continue
		}
		result = append(result, developmentApplicationIdentity{
			PID:     pid,
			Created: strings.Join(fields[2:7], " "),
		})
	}
	return result, nil
}
