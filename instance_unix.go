//go:build darwin || linux

package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func instanceEndpoint(directory string) (string, error) {
	return filepath.Join(directory, "ipc.sock"), nil
}

func lockInstance(directory string) (func() error, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	endpoint, err := privateSocketPath(filepath.Join(directory, "ipc.sock"))
	if err != nil {
		return nil, err
	}
	path := filepath.Join(filepath.Dir(endpoint), "runtime.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		_ = file.Close()
		return nil, fmt.Errorf("unsafe instance lock: %w", os.ErrPermission)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	// Keep the lock file: unlinking it would let contenders lock different inodes.
	return file.Close, nil
}
