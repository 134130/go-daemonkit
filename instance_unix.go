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
	// Resolve existing ancestors without creating the instance directory.
	parent, suffix := directory, ""
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			if err := validateSocketPathLength(filepath.Join(resolved, suffix, "ipc.sock")); err != nil {
				return "", err
			}
			return filepath.Join(directory, "ipc.sock"), nil
		}
		if !errors.Is(err, os.ErrNotExist) || parent == filepath.Dir(parent) {
			return "", err
		}
		suffix = filepath.Join(filepath.Base(parent), suffix)
		parent = filepath.Dir(parent)
	}
}

func lockInstance(directory string) (func() error, error) {
	return lockNamedInstance(directory, "runtime")
}

func lockNamedInstance(directory, name string) (func() error, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	endpoint, err := privateSocketPath(filepath.Join(directory, "ipc.sock"))
	if err != nil {
		return nil, err
	}
	path := filepath.Join(filepath.Dir(endpoint), name+".lock")
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

func validateSocketPathLength(path string) error {
	limit := len(unix.RawSockaddrUnix{}.Path) - 1
	if len(path) > limit {
		return fmt.Errorf("unix socket path is %d bytes; maximum is %d: shorten XDG_STATE_HOME, appID, or instanceID", len(path), limit)
	}
	return nil
}
