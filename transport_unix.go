//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// The runtime creates the private directory. Stale endpoint removal requires
// process ownership checks and is deliberately not performed by the transport.
func listenLocal(path string) (net.Listener, error) {
	path, err := privateSocketPath(path)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	// net.UnixListener otherwise unlinks by name, even if the file was replaced.
	conn.SetUnlinkOnClose(false)
	info, err := os.Lstat(path)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	listener := &localListener{UnixListener: conn, path: path, identity: info}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

func dialLocal(ctx context.Context, path string) (net.Conn, error) {
	path, err := privateSocketPath(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("unsafe local socket %q: %w", path, os.ErrPermission)
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", path)
}

func privateSocketPath(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("socket path must be absolute and clean")
	}
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return "", fmt.Errorf("socket directory must be owned by the current user with mode 0700: %w", os.ErrPermission)
	}
	// Resolve OS aliases such as macOS /tmp, then reject replaceable ancestors.
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	for dir := filepath.Dir(parent); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err != nil {
			return "", err
		}
		uid := info.Sys().(*syscall.Stat_t).Uid
		trustedOwner := uid == 0 || uid == uint32(os.Geteuid())
		if !trustedOwner || (info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return "", fmt.Errorf("unsafe socket ancestor %q: %w", dir, os.ErrPermission)
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	path = filepath.Join(parent, filepath.Base(path))
	if err := validateSocketPathLength(path); err != nil {
		return "", err
	}
	return path, nil
}

type localListener struct {
	*net.UnixListener
	path      string
	identity  os.FileInfo
	closeOnce sync.Once
	closeErr  error
}

func (l *localListener) Close() error {
	l.closeOnce.Do(func() {
		l.closeErr = l.UnixListener.Close()
		info, err := os.Lstat(l.path)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			l.closeErr = errors.Join(l.closeErr, err)
			return
		}
		if os.SameFile(l.identity, info) {
			l.closeErr = errors.Join(l.closeErr, os.Remove(l.path))
		}
	})
	return l.closeErr
}
