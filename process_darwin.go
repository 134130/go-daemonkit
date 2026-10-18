package daemon

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func processIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", os.ErrNotExist
	}
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if len(processes) == 0 {
		return "", os.ErrNotExist
	}
	boot, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return "", err
	}
	start := processes[0].Proc.P_starttime
	return fmt.Sprintf("%s:%d:%d", boot, start.Sec, start.Usec), nil
}
