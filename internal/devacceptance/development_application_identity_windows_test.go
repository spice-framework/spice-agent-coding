//go:build windows

package devacceptance

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

type developmentApplicationIdentity struct {
	PID     uint32
	Created int64
}

func directDevelopmentApplicationIdentities(parentPID int) ([]developmentApplicationIdentity, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("snapshot development processes: %w", err)
	}
	defer windows.CloseHandle(snapshot) //nolint:errcheck // Read-only test snapshot cleanup.
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err = windows.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("read first development process: %w", err)
	}
	var result []developmentApplicationIdentity
	for {
		if entry.ParentProcessID == uint32(parentPID) && // #nosec G115 -- positive OS process identifier.
			windows.UTF16ToString(entry.ExeFile[:]) == "application.exe" {
			identity, identityErr := windowsDevelopmentIdentity(entry.ProcessID)
			if identityErr == nil {
				result = append(result, identity)
			}
		}
		err = windows.Process32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return result, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read next development process: %w", err)
		}
	}
}

func windowsDevelopmentIdentity(pid uint32) (developmentApplicationIdentity, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return developmentApplicationIdentity{}, err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // Read-only test process handle cleanup.
	var created, exited, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return developmentApplicationIdentity{}, err
	}
	return developmentApplicationIdentity{PID: pid, Created: created.Nanoseconds()}, nil
}
