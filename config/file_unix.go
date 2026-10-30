//go:build linux || darwin

package config

import (
	"golang.org/x/sys/unix"
	"os"
)

func openRead(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
func privateFile(file *os.File) error { return file.Chmod(0600) }

func privateDirectory(path string) error { return os.Chmod(path, 0700) }
