//go:build windows

package devprobe

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func replacePublishedAddress(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = windows.MoveFileEx(
			from,
			to,
			windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
		)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return os.ErrNotExist
		}
		if err == nil || !transientPublishedAddressContention(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func transientPublishedAddressContention(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
