package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
)

func connectTestServer(t *testing.T, serve func(*websocket.Conn) error) (*websocket.Conn, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer conn.CloseNow()
		done <- serve(conn)
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn, done
}

func awaitHandshake(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("server did not finish handshake")
		return nil
	}
}

func TestWebSocketHandshake(t *testing.T) {
	params, identity := validHandshake()
	conn, done := connectTestServer(t, func(conn *websocket.Conn) error {
		if err := initializeServer(t.Context(), conn, identity); err != nil {
			return err
		}
		// The handshake's context is canceled on return; the connection survives.
		message, err := readRPC(t.Context(), conn)
		if err != nil {
			return err
		}
		return writeRPC(t.Context(), conn, rpcMessage{JSONRPC: "2.0", ID: message.ID, Result: json.RawMessage(`true`)})
	})
	got, err := initializeClient(t.Context(), conn, params)
	if err != nil || got != identity {
		t.Fatalf("initialize = %+v, %v", got, err)
	}
	if err := writeRPC(t.Context(), conn, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "test"}); err != nil {
		t.Fatal(err)
	}
	response, err := readRPC(t.Context(), conn)
	if err != nil || string(response.Result) != "true" {
		t.Fatalf("post-handshake response = %+v, %v", response, err)
	}
	if err := awaitHandshake(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestWebSocketHandshakeRejectsMessages(t *testing.T) {
	_, identity := validHandshake()
	for _, tc := range []struct {
		name    string
		payload string
		code    int
	}{
		{"parse", `{`, -32700},
		{"version", `{"jsonrpc":"1.0","id":1,"method":"initialize"}`, -32600},
		{"earlyRequest", `{"jsonrpc":"2.0","id":1,"method":"work"}`, -32600},
		{"invalidID", `{"jsonrpc":"2.0","id":{},"method":"initialize"}`, -32600},
		{"badParams", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":[]}`, -32602},
		{"mismatch", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"appID":"other","instanceID":"default","protocolVersion":1,"clientVersion":"v1"}}`, -32602},
		{"notification", `{"jsonrpc":"2.0","method":"initialized"}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, done := connectTestServer(t, func(conn *websocket.Conn) error {
				return initializeServer(t.Context(), conn, identity)
			})
			if err := conn.Write(t.Context(), websocket.MessageText, []byte(tc.payload)); err != nil {
				t.Fatal(err)
			}
			response, err := readRPC(t.Context(), conn)
			if tc.code == 0 {
				if err == nil {
					t.Fatal("notification received a response")
				}
			} else if err != nil || response.Error == nil || response.Error.Code != tc.code {
				t.Fatalf("response = %+v, %v; want code %d", response, err, tc.code)
			}
			if awaitHandshake(t, done) == nil {
				t.Fatal("invalid handshake accepted")
			}
		})
	}
}

func TestWebSocketHandshakeWaitsForInitialized(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		params, identity := validHandshake()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		conn, done := connectPipeTestServer(t, func(conn *websocket.Conn) error {
			return initializeServer(ctx, conn, identity)
		})
		encoded, _ := json.Marshal(params)
		if err := writeRPC(t.Context(), conn, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize", Params: encoded}); err != nil {
			t.Fatal(err)
		}
		if _, err := readRPC(t.Context(), conn); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("server completed before initialized: %v", err)
		default:
		}
		cancel()
		if err := awaitHandshake(t, done); !errors.Is(err, context.Canceled) {
			t.Fatalf("server error = %v; want cancellation", err)
		}
	})
}

func TestWebSocketHandshakeDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, identity := validHandshake()
		start := time.Now()
		_, done := connectPipeTestServer(t, func(conn *websocket.Conn) error {
			return initializeServer(t.Context(), conn, identity)
		})
		if err := (<-done); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("server error = %v; want deadline", err)
		}
		if elapsed := time.Since(start); elapsed != handshakeTimeout {
			t.Fatalf("elapsed = %v; want %v", elapsed, handshakeTimeout)
		}
	})
}

func TestClientRejectsWrongResponse(t *testing.T) {
	params, _ := validHandshake()
	for _, payload := range []string{
		`{"jsonrpc":"2.0","id":"other","result":{}}`,
		`{"jsonrpc":"2.0","id":"initialize","result":{},"error":{"code":-32600,"message":"bad"}}`,
		`{"jsonrpc":"2.0","id":"initialize","result":{},"error":null}`,
		`{"jsonrpc":"2.0","id":"initialize","error":{}}`,
		`{"jsonrpc":"2.0","id":"initialize","result":{"appID":"app","instanceID":"default","protocolVersion":2,"serverVersion":"v1","launchNonce":"x"}}`,
	} {
		t.Run(payload, func(t *testing.T) {
			conn, done := connectTestServer(t, func(conn *websocket.Conn) error {
				if _, err := readRPC(t.Context(), conn); err != nil {
					return err
				}
				return conn.Write(t.Context(), websocket.MessageText, []byte(payload))
			})
			if _, err := initializeClient(t.Context(), conn, params); err == nil {
				t.Fatal("invalid response accepted")
			}
			if err := awaitHandshake(t, done); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestServerRejectsSecondInitialize(t *testing.T) {
	params, identity := validHandshake()
	conn, done := connectTestServer(t, func(conn *websocket.Conn) error {
		return initializeServer(t.Context(), conn, identity)
	})
	encoded, _ := json.Marshal(params)
	request := rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize", Params: encoded}
	for range 2 {
		if err := writeRPC(t.Context(), conn, request); err != nil {
			t.Fatal(err)
		}
		response, err := readRPC(t.Context(), conn)
		if err != nil {
			t.Fatal(err)
		}
		if response.Error != nil && response.Error.Code != -32600 {
			t.Fatalf("unexpected rejection: %v", response.Error)
		}
	}
	if awaitHandshake(t, done) == nil {
		t.Fatal("second initialize accepted")
	}
}

func TestHandshakeMessageLimit(t *testing.T) {
	_, identity := validHandshake()
	conn, done := connectTestServer(t, func(conn *websocket.Conn) error {
		return initializeServer(t.Context(), conn, identity)
	})
	// Writing may fail when the server closes before the whole message is sent.
	_ = conn.Write(t.Context(), websocket.MessageText, []byte(strings.Repeat("x", handshakeReadLimit+1)))
	_, _, _ = conn.Read(t.Context())
	if awaitHandshake(t, done) == nil {
		t.Fatal("oversized message accepted")
	}
}
