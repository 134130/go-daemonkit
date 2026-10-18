package daemon

import (
	"errors"
	"golang.org/x/sys/unix"
)

func killOwnedProcess(record executionRecord) error {
	fd, err := unix.PidfdOpen(record.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	alive, err := processMatches(record.PID, record.ProcessIdentity)
	if err != nil || !alive {
		return err
	}
	err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
