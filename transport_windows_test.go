package daemon

import (
	"crypto/rand"
	"testing"
)

func testLocalPath(t *testing.T) string {
	t.Helper()
	return `\\.\pipe\daemonkit-test-` + rand.Text()
}

func TestLocalPipeRejectsRemotePaths(t *testing.T) {
	for _, path := range []string{`\\remote\pipe\daemon`, `\\.\pipe\`, `daemon`, `\\.\pipe\nested\daemon`} {
		if listener, err := listenLocal(path); err == nil {
			_ = listener.Close()
			t.Fatalf("invalid path accepted: %q", path)
		}
		if conn, err := dialLocal(t.Context(), path); err == nil {
			_ = conn.Close()
			t.Fatalf("invalid dial path accepted: %q", path)
		}
	}
}

func TestLocalPipePreservesExistingListener(t *testing.T) {
	path := testLocalPath(t)
	listener, err := listenLocal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if other, err := listenLocal(path); err == nil {
		_ = other.Close()
		t.Fatal("existing pipe replaced")
	}
}
