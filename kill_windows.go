package daemon

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
)

func killOwnedProcess(record executionRecord) error {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(record.PID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	var start, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &start, &exit, &kernel, &user); err != nil {
		return err
	}
	if exit.HighDateTime != 0 || exit.LowDateTime != 0 {
		return nil
	}
	if fmt.Sprintf("%d:%d", start.HighDateTime, start.LowDateTime) != record.ProcessIdentity {
		return nil
	}
	return windows.TerminateProcess(handle, 1)
}
