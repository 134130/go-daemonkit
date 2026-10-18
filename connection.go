package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/coder/websocket"
)

const (
	handshakeTimeout   = 5 * time.Second
	handshakeReadLimit = 64 << 10
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("RPC error %d: %s", e.Code, e.Message)
}

// The caller owns conn after success and must keep a reader active. On failure
// the connection is closed. The deadline covers the whole initialization.
func initializeClient(ctx context.Context, conn *websocket.Conn, params initializeParams) (result initializeResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	defer func() {
		if err != nil {
			_ = conn.CloseNow()
		}
	}()
	conn.SetReadLimit(handshakeReadLimit)
	if err = params.validate(); err != nil {
		return result, err
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return result, err
	}
	const requestID = `"initialize"`
	if err = writeRPC(ctx, conn, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(requestID), Method: "initialize", Params: encoded}); err != nil {
		return result, err
	}
	response, err := readRPC(ctx, conn)
	if err != nil {
		return result, err
	}
	var id string
	if json.Unmarshal(response.ID, &id) != nil || id != "initialize" || response.Method != "" || len(response.Params) != 0 || (response.Error == nil) == (len(response.Result) == 0) {
		return result, errors.New("initialize: invalid response")
	}
	if response.Error != nil {
		return result, response.Error
	}
	if err = json.Unmarshal(response.Result, &result); err != nil {
		return result, err
	}
	if err = result.validate(params); err != nil {
		return result, err
	}
	err = writeRPC(ctx, conn, rpcMessage{JSONRPC: "2.0", Method: "initialized"})
	return result, err
}

// Mandatory daemon initialization must finish before calling initializeServer.
// No server push is permitted until this function returns successfully.
func initializeServer(ctx context.Context, conn *websocket.Conn, server initializeResult) (err error) {
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	defer func() {
		if err != nil {
			_ = conn.CloseNow()
		}
	}()
	conn.SetReadLimit(handshakeReadLimit)
	var h handshake
	request, err := readRPC(ctx, conn)
	if err != nil {
		return replyReadError(ctx, conn, err)
	}
	if request.Method != "initialize" || !validRequestID(request.ID) || request.Error != nil || len(request.Result) != 0 {
		return rejectHandshake(ctx, conn, request, -32600, "expected initialize request")
	}
	var params initializeParams
	if err = json.Unmarshal(request.Params, &params); err != nil {
		return rejectHandshake(ctx, conn, request, -32602, "invalid initialize parameters")
	}
	result, err := h.initialize(params, server)
	if err != nil {
		return rejectHandshake(ctx, conn, request, -32602, err.Error())
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err = writeRPC(ctx, conn, rpcMessage{JSONRPC: "2.0", ID: request.ID, Result: encoded}); err != nil {
		return err
	}
	request, err = readRPC(ctx, conn)
	if err != nil {
		return replyReadError(ctx, conn, err)
	}
	if request.Method != "initialized" || len(request.ID) != 0 || len(request.Result) != 0 || request.Error != nil || !emptyParams(request.Params) {
		return rejectHandshake(ctx, conn, request, -32600, "expected initialized notification")
	}
	return h.initialized()
}

func readRPC(ctx context.Context, conn *websocket.Conn) (rpcMessage, error) {
	kind, payload, err := conn.Read(ctx)
	if err != nil {
		return rpcMessage{}, err
	}
	if kind != websocket.MessageText {
		return rpcMessage{}, &rpcError{-32600, "expected text message"}
	}
	if !json.Valid(payload) {
		return rpcMessage{}, &rpcError{-32700, "invalid JSON"}
	}
	var message rpcMessage
	if err := json.Unmarshal(payload, &message); err != nil || message.JSONRPC != "2.0" {
		return rpcMessage{}, &rpcError{-32600, "invalid JSON-RPC message"}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return rpcMessage{}, &rpcError{-32600, "expected JSON-RPC object"}
	}
	if _, exists := fields["method"]; exists && message.Method == "" {
		return rpcMessage{}, &rpcError{-32600, "invalid method"}
	}
	if raw, exists := fields["error"]; exists {
		var failure map[string]json.RawMessage
		if json.Unmarshal(raw, &failure) != nil || message.Error == nil || failure["code"] == nil || failure["message"] == nil || bytes.Equal(failure["code"], []byte("null")) || bytes.Equal(failure["message"], []byte("null")) {
			return rpcMessage{}, &rpcError{-32600, "invalid error object"}
		}
	}
	return message, nil
}

func writeRPC(ctx context.Context, conn *websocket.Conn, message rpcMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}

func validRequestID(id json.RawMessage) bool {
	if len(id) == 0 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(id))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	switch value.(type) {
	case string, json.Number:
		return true
	default:
		return false
	}
}

func emptyParams(params json.RawMessage) bool {
	if len(params) == 0 {
		return true
	}
	var fields map[string]json.RawMessage
	return json.Unmarshal(params, &fields) == nil && fields != nil && len(fields) == 0
}

func rejectHandshake(ctx context.Context, conn *websocket.Conn, request rpcMessage, code int, reason string) error {
	err := &rpcError{code, reason}
	// Valid notifications have no response, including out-of-order ones.
	if len(request.ID) == 0 && request.Method != "" && request.Error == nil && len(request.Result) == 0 {
		return err
	}
	id := request.ID
	if !validRequestID(id) {
		id = json.RawMessage("null")
	}
	return errors.Join(err, writeRPC(ctx, conn, rpcMessage{JSONRPC: "2.0", ID: id, Error: err}))
}

func replyReadError(ctx context.Context, conn *websocket.Conn, err error) error {
	var failure *rpcError
	if errors.As(err, &failure) {
		return errors.Join(err, writeRPC(ctx, conn, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: failure}))
	}
	return err
}
