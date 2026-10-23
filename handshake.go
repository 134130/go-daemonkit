package daemon

import "fmt"

type handshakeState uint8

const (
	handshakeNew handshakeState = iota
	handshakeAwaitingInitialized
	handshakeReady
	handshakeClosed
)

// A connection's reader owns its handshake and processes transitions in order.
// This is connection readiness, not daemon readiness. The caller admits a
// successful initialize only after mandatory daemon startup has completed.
type handshake struct {
	state handshakeState
}

func (h *handshake) initialize(params initializeParams, server initializeResult) (initializeResult, error) {
	if h.state != handshakeNew {
		return initializeResult{}, h.reject("unexpected initialize")
	}
	if err := server.validate(params); err != nil {
		h.close()
		return initializeResult{}, err
	}
	h.state = handshakeAwaitingInitialized
	return server, nil
}

func (h *handshake) initialized() error {
	if h.state != handshakeAwaitingInitialized {
		return h.reject("unexpected initialized")
	}
	h.state = handshakeReady
	return nil
}

// Server-originated requests and notifications must wait for this condition.
func (h *handshake) ready() bool {
	return h.state == handshakeReady
}

func (h *handshake) close() {
	h.state = handshakeClosed
}

func (h *handshake) reject(reason string) error {
	h.close()
	return fmt.Errorf("handshake: %s", reason)
}
