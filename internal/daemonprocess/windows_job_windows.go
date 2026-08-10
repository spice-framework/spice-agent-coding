//go:build windows

package daemonprocess

import (
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsJob struct{}

func (windowsJob) open() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), // #nosec G103 -- Windows requires a pointer to the exact typed limits structure.
		uint32(unsafe.Sizeof(limits)),    // #nosec G115 -- static Windows structure size.
	)
	if err != nil {
		return 0, errors.Join(err, (windowsHandleOwner{}).close(job))
	}
	return job, nil
}

func (windowsJob) waitEmpty(job windows.Handle, timeout time.Duration) error {
	if job == 0 || job == windows.InvalidHandle {
		return errors.New("managed daemon Windows job handle is invalid")
	}
	deadline := time.Now().Add(timeout)
	for {
		accounting := struct {
			TotalUserTime             int64
			TotalKernelTime           int64
			ThisPeriodTotalUserTime   int64
			ThisPeriodTotalKernelTime int64
			TotalPageFaultCount       uint32
			TotalProcesses            uint32
			ActiveProcesses           uint32
			TotalTerminatedProcesses  uint32
		}{}
		err := windows.QueryInformationJobObject(
			job,
			windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&accounting)), // #nosec G103 -- Windows requires a pointer to the exact typed accounting structure.
			uint32(unsafe.Sizeof(accounting)),    // #nosec G115 -- static Windows structure size.
			nil,
		)
		if err != nil {
			return err
		}
		if accounting.ActiveProcesses == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.New("managed daemon Windows job cleanup timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
