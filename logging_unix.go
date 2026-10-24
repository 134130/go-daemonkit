//go:build darwin || linux

package daemon

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openLogFile(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid())) {
		err = fmt.Errorf("unsafe log file: %w", os.ErrPermission)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
