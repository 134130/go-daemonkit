//go:build darwin || linux

package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func socketIdentity(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	stat := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSocket == 0 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("unsafe socket identity: %w", os.ErrPermission)
	}
	return fmt.Sprintf("%d:%d:%d", stat.Dev, stat.Ino, info.ModTime().UnixNano()), nil
}

// The canonical endpoint appears only after its identity has been recorded.
func prepareExecutionEndpoint(path, nonce string) (net.Listener, string, func() error, error) {
	canonical, err := privateSocketPath(path)
	if err != nil {
		return nil, "", nil, err
	}
	staging := filepath.Join(filepath.Dir(canonical), nonce[:8])
	listener, err := listenLocal(staging)
	if err != nil {
		return nil, "", nil, err
	}
	identity, err := socketIdentity(staging)
	if err != nil {
		_ = listener.Close()
		return nil, "", nil, err
	}
	publish := func() error {
		if _, err := os.Lstat(canonical); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return fmt.Errorf("endpoint already exists")
		}
		if err := os.Rename(staging, canonical); err != nil {
			return err
		}
		listener.(*localListener).path = canonical
		return nil
	}
	return listener, identity, publish, nil
}

func removeStaleEndpoint(path string, record executionRecord) error {
	identity, err := socketIdentity(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if identity != record.SocketIdentity {
		return fmt.Errorf("socket ownership does not match execution record")
	}
	conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("recorded socket still accepts connections")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return err
	}
	current, err := socketIdentity(path)
	if err != nil {
		return err
	}
	if current != identity {
		return fmt.Errorf("socket changed during recovery")
	}
	return os.Remove(path)
}

func recoverEndpoints(path string, record executionRecord) error {
	if err := removeStaleEndpoint(path, record); err != nil {
		return err
	}
	// A crash after recording but before publication leaves the staging socket.
	if len(record.LaunchNonce) >= 8 && validName(record.LaunchNonce[:8]) {
		staging := filepath.Join(filepath.Dir(path), record.LaunchNonce[:8])
		if staging != path {
			return removeStaleEndpoint(staging, record)
		}
	}
	return nil
}
