package daemon

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func processIdentity(pid int) (string, error) {
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		return "", os.ErrNotExist
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return "", os.ErrNotExist
	}
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var start, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &start, &exit, &kernel, &user); err != nil {
		return "", err
	}
	if exit.HighDateTime != 0 || exit.LowDateTime != 0 {
		return "", os.ErrNotExist
	}
	return fmt.Sprintf("%d:%d", start.HighDateTime, start.LowDateTime), nil
}
