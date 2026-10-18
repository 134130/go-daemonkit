package daemon

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

// net.Pipe blocks on channels, allowing synctest to advance virtual time.
type pipeListener struct {
	conn   net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	if l.conn != nil {
		conn := l.conn
		l.conn = nil
		return conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (*pipeListener) Addr() net.Addr { return &net.UnixAddr{Name: "test", Net: "unix"} }

func connectPipeTestServer(t *testing.T, serve func(*websocket.Conn) error) (*websocket.Conn, <-chan error) {
	t.Helper()
	serverSide, clientSide := net.Pipe()
	t.Cleanup(func() { _ = serverSide.Close(); _ = clientSide.Close() })
	listener := &pipeListener{conn: serverSide, closed: make(chan struct{})}
	done := make(chan error, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer conn.CloseNow()
		done <- serve(conn)
	})}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return clientSide, nil
	}}
	t.Cleanup(transport.CloseIdleConnections)
	conn, _, err := websocket.Dial(t.Context(), "ws://localhost/", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn, done
}
