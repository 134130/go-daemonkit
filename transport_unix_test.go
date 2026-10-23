//go:build darwin || linux

package daemon

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLocalPath(t *testing.T) string {
	t.Helper()
	// Keep below macOS's sockaddr_un path length limit.
	dir, err := os.MkdirTemp("/tmp", "dk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "ipc.sock")
}

func TestLocalSocketPermissions(t *testing.T) {
	path := testLocalPath(t)
	listener, err := listenLocal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions: %v, %v", info, err)
	}
	if other, err := listenLocal(path); err == nil {
		_ = other.Close()
		t.Fatal("second listener replaced existing endpoint")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned socket was not removed: %v", err)
	}
}

func TestLocalSocketRejectsUnsafeDirectory(t *testing.T) {
	path := testLocalPath(t)
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if listener, err := listenLocal(path); !errors.Is(err, os.ErrPermission) {
		if listener != nil {
			_ = listener.Close()
		}
		t.Fatalf("unsafe directory accepted: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket created in unsafe directory: %v", err)
	}
}

func TestLocalSocketPreservesReplacement(t *testing.T) {
	path := testLocalPath(t)
	listener, err := listenLocal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("replacement modified: %q, %v", data, err)
	}
}

func TestLocalSocketRejectsUnsafeSocket(t *testing.T) {
	path := testLocalPath(t)
	listener, err := listenLocal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if conn, err := dialLocal(t.Context(), path); !errors.Is(err, os.ErrPermission) {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatalf("unsafe socket accepted: %v", err)
	}
}

func TestLocalSocketRejectsSymlinkDirectory(t *testing.T) {
	path := testLocalPath(t)
	alias := filepath.Join(filepath.Dir(path), "alias")
	if err := os.Symlink(filepath.Dir(path), alias); err != nil {
		t.Fatal(err)
	}
	if listener, err := listenLocal(filepath.Join(alias, "other.sock")); !errors.Is(err, os.ErrPermission) {
		if listener != nil {
			_ = listener.Close()
		}
		t.Fatalf("symlink directory accepted: %v", err)
	}
}

func TestLocalSocketRejectsUnsafeAncestor(t *testing.T) {
	path := testLocalPath(t)
	parent := filepath.Dir(path)
	private := filepath.Join(parent, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	if listener, err := listenLocal(filepath.Join(private, "ipc.sock")); !errors.Is(err, os.ErrPermission) {
		if listener != nil {
			_ = listener.Close()
		}
		t.Fatalf("replaceable ancestor accepted: %v", err)
	}
}

// Validate canonical byte length, including aliases such as /tmp -> /private/tmp.
func TestInstanceEndpointLength(t *testing.T) {
	root := filepath.Dir(testLocalPath(t))
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	limit := len(unix.RawSockaddrUnix{}.Path) - 1
	for _, extra := range []int{0, 1} {
		name := strings.Repeat("a", limit-len(resolved)-len("/ipc.sock")-1+extra)
		directory := filepath.Join(root, name)
		path, err := instanceEndpoint(directory)
		if extra == 0 && (err != nil || path != filepath.Join(directory, "ipc.sock")) {
			t.Fatalf("boundary path = %q, %v", path, err)
		}
		if extra == 1 && (err == nil || !strings.Contains(err.Error(), "shorten")) {
			t.Fatalf("long path error = %v", err)
		}
		if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("validation created directory: %v", err)
		}
	}
	t.Setenv("XDG_STATE_HOME", root)
	if _, err := New(Options{AppID: strings.Repeat("a", 64), InstanceID: strings.Repeat("b", 64), Version: "test"}); err == nil {
		t.Fatal("New accepted an unusable socket path")
	}
	// A short alias to a long existing ancestor must not bypass the check.
	long := filepath.Join(root, strings.Repeat("x", limit))
	if err := os.Mkdir(long, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(long, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := instanceEndpoint(filepath.Join(alias, "missing", "child")); err == nil {
		t.Fatal("accepted long canonical path behind a short alias")
	}
}
