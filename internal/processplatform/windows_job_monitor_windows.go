//go:build windows

package processplatform

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsJobMonitor struct{}

func (windowsJobMonitor) waitEmpty(job windows.Handle) error {
	if job == 0 || job == windows.InvalidHandle {
		return errors.New("windows process job is invalid")
	}
	for {
		accounting := struct {
			TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
			TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
		}{}
		err := windows.QueryInformationJobObject(
			job, windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&accounting)), // #nosec G103 -- exact Windows accounting structure.
			uint32(unsafe.Sizeof(accounting)),    // #nosec G115 -- static Windows structure size.
			nil,
		)
		if err != nil {
			return err
		}
		if accounting.ActiveProcesses == 0 {
			return nil
		}
		time.Sleep(windowsJobPollInterval)
	}
}

func (windowsJobMonitor) waitHandle(handle windows.Handle, timeout time.Duration) error {
	if handle == 0 || handle == windows.InvalidHandle {
		return errors.New("windows process handle is invalid")
	}
	milliseconds := uint32(windows.INFINITE)
	if timeout > 0 {
		milliseconds = uint32(min(timeout.Milliseconds(), int64(windows.INFINITE-1))) // #nosec G115 -- capped below uint32 maximum.
	}
	event, err := windows.WaitForSingleObject(handle, milliseconds)
	if err != nil {
		return err
	}
	if event == windows.WAIT_OBJECT_0 {
		return nil
	}
	if event == uint32(windows.WAIT_TIMEOUT) {
		return errors.New("windows process cleanup timed out")
	}
	return fmt.Errorf("unexpected Windows process wait result: %d", event)
}
