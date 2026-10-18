//go:build darwin || linux

package daemon

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/coder/websocket"
)

func TestHandshakeOverUnixSocket(t *testing.T) {
	// A short private path avoids macOS's sockaddr_un length limit.
	dir, err := os.MkdirTemp("/tmp", "dk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "ipc.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	params, identity := validHandshake()
	done := make(chan error, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer conn.CloseNow()
		done <- initializeServer(t.Context(), conn, identity)
	})}
	t.Cleanup(func() { _ = server.Close() })
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-serveDone
	})
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	conn, _, err := websocket.Dial(t.Context(), "ws://localhost/", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	result, err := initializeClient(t.Context(), conn, params)
	if err != nil || result != identity {
		t.Fatalf("initialize = %+v, %v", result, err)
	}
	if err := awaitHandshake(t, done); err != nil {
		t.Fatal(err)
	}
}
