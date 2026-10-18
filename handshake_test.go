package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

func validHandshake() (initializeParams, initializeResult) {
	return initializeParams{"app", "default", protocolVersion, "1.0.0"},
		initializeResult{"app", "default", protocolVersion, "2.0.0", "run-1"}
}

func TestHandshake(t *testing.T) {
	params, server := validHandshake()
	var h handshake
	if h.ready() {
		t.Fatal("new connection allowed server push")
	}
	got, err := h.initialize(params, server)
	if err != nil || got != server {
		t.Fatalf("initialize = %+v, %v", got, err)
	}
	if h.ready() {
		t.Fatal("initialize response allowed server push before initialized")
	}
	if err := h.initialized(); err != nil {
		t.Fatal(err)
	}
	if !h.ready() {
		t.Fatal("initialized did not enable server push")
	}
	for range 2 {
		if err := h.admitRequest(); err != nil {
			t.Fatal(err)
		}
	}
	h.close()
	if h.ready() || h.admitRequest() == nil {
		t.Fatal("closed connection admitted work")
	}
}

func TestHandshakeRejectsInvalidIdentity(t *testing.T) {
	for _, field := range []string{"app", "instance", "clientProtocol", "serverProtocol", "clientVersion", "serverVersion", "nonce"} {
		t.Run(field, func(t *testing.T) {
			params, server := validHandshake()
			switch field {
			case "app":
				params.AppID = "other"
			case "instance":
				params.InstanceID = "other"
			case "clientProtocol":
				params.ProtocolVersion++
			case "serverProtocol":
				server.ProtocolVersion++
			case "clientVersion":
				params.ClientVersion = ""
			case "serverVersion":
				server.ServerVersion = ""
			case "nonce":
				server.LaunchNonce = ""
			}
			var h handshake
			if _, err := h.initialize(params, server); err == nil {
				t.Fatal("invalid handshake accepted")
			}
			if h.state != handshakeClosed {
				t.Fatal("invalid handshake did not close connection state")
			}
		})
	}
}

func TestHandshakeRejectsOutOfOrderMessages(t *testing.T) {
	for _, sequence := range []string{"ready", "request", "init init", "init request", "init ready ready", "init ready init", "close init"} {
		t.Run(sequence, func(t *testing.T) {
			params, server := validHandshake()
			var h handshake
			var err error
			for _, step := range strings.Fields(sequence) {
				switch step {
				case "init":
					_, err = h.initialize(params, server)
				case "ready":
					err = h.initialized()
				case "request":
					err = h.admitRequest()
				case "close":
					h.close()
				}
			}
			if err == nil || h.state != handshakeClosed {
				t.Fatalf("sequence accepted: state=%v, error=%v", h.state, err)
			}
		})
	}
}

func TestHandshakeConnectionsAreIndependent(t *testing.T) {
	params, server := validHandshake()
	var first, second handshake
	if _, err := first.initialize(params, server); err != nil {
		t.Fatal(err)
	}
	if err := first.initialized(); err != nil {
		t.Fatal(err)
	}
	if second.ready() || !first.ready() {
		t.Fatal("connection readiness leaked")
	}
	second.close()
	if !first.ready() {
		t.Fatal("closing another connection changed readiness")
	}
}

func TestInitializeWireFields(t *testing.T) {
	params, server := validHandshake()
	for _, tc := range []struct {
		value any
		want  string
	}{
		{params, `{"appID":"app","instanceID":"default","protocolVersion":1,"clientVersion":"1.0.0"}`},
		{server, `{"appID":"app","instanceID":"default","protocolVersion":1,"serverVersion":"2.0.0","launchNonce":"run-1"}`},
	} {
		got, err := json.Marshal(tc.value)
		if err != nil || string(got) != tc.want {
			t.Fatalf("wire payload = %s, %v; want %s", got, err, tc.want)
		}
	}
}
