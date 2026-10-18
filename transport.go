//go:build darwin || linux || windows

package daemon

import (
	"context"
	"net"
	"net/http"

	"github.com/coder/websocket"
)

// connectLocal bounds transport setup and RPC initialization by one deadline.
func connectLocal(ctx context.Context, path string, params initializeParams) (*websocket.Conn, initializeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialLocal(ctx, path)
		},
	}
	defer transport.CloseIdleConnections()
	conn, response, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{
		HTTPClient: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	})
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, initializeResult{}, err
	}
	result, err := initializeClient(ctx, conn, params)
	if err != nil {
		return nil, initializeResult{}, err
	}
	return conn, result, nil
}
