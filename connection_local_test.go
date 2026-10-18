//go:build darwin || linux || windows

package daemon

import (
	"net/http"
	"testing"

	"github.com/coder/websocket"
)

func TestHandshakeOverLocalTransport(t *testing.T) {
	path := testLocalPath(t)
	listener, err := listenLocal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
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
	conn, result, err := connectLocal(t.Context(), path, params)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if result != identity {
		t.Fatalf("initialize = %+v; want %+v", result, identity)
	}
	if err := awaitHandshake(t, done); err != nil {
		t.Fatal(err)
	}
}
