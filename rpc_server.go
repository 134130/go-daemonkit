package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const maxRPCRequests = 64

// LSP cancellation is a framework extension, not part of JSON-RPC 2.0.
const cancelRPCMethod = "$/cancelRequest"

type rpcMethod struct {
	call  reflect.Value
	input reflect.Type
}

type rpcServer struct {
	methods  map[string]rpcMethod
	mu       sync.Mutex
	stopping bool
	active   int
	calls    sync.WaitGroup
	stop     chan struct{}
}

func newRPCServer(services []registeredService) *rpcServer {
	server := &rpcServer{methods: make(map[string]rpcMethod), stop: make(chan struct{})}
	for _, service := range services {
		value := reflect.ValueOf(service.value)
		for i := range value.NumMethod() {
			name := value.Type().Method(i).Name
			if name == "Startup" || name == "Shutdown" {
				continue
			}
			method := value.Method(i)
			server.methods[service.name+"."+name] = rpcMethod{method, method.Type().In(1)}
		}
	}
	return server
}

func (s *rpcServer) serve(ctx context.Context, conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var pendingMu sync.Mutex
	pending := make(map[string]context.CancelFunc)
	for {
		request, err := readRPC(ctx, conn)
		if err != nil {
			_ = replyReadError(ctx, conn, err)
			return
		}
		if request.Method == "initialize" || request.Method == "initialized" || request.Method == "" || request.Error != nil || len(request.Result) != 0 || len(request.ID) != 0 && !validRequestID(request.ID) {
			_ = rejectHandshake(ctx, conn, request, -32600, "invalid request or initialization sequence")
			return
		}
		if request.Method == cancelRPCMethod && len(request.ID) == 0 {
			var params struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(request.Params, &params) != nil || !validRequestID(params.ID) {
				return
			}
			pendingMu.Lock()
			if cancel := pending[requestKey(params.ID)]; cancel != nil {
				cancel()
			}
			pendingMu.Unlock()
			continue
		}
		// Only cancellation notifications are handled; application methods require IDs.
		if len(request.ID) == 0 {
			continue
		}
		key := requestKey(request.ID)
		pendingMu.Lock()
		_, duplicate := pending[key]
		pendingMu.Unlock()
		if duplicate {
			_ = replyRPCError(ctx, conn, request.ID, &RPCError{Code: -32600, Message: "duplicate request ID"})
			return
		}
		if request.Method == "shutdown" {
			if !emptyParams(request.Params) {
				if replyRPCError(ctx, conn, request.ID, &RPCError{Code: -32602, Message: "shutdown takes no parameters"}) != nil {
					return
				}
				continue
			}
			s.mu.Lock()
			first := !s.stopping
			s.stopping = true
			s.mu.Unlock()
			if !first {
				if replyRPCError(ctx, conn, request.ID, stoppingRPCError()) != nil {
					return
				}
				continue
			}
			replyCtx, end := context.WithTimeout(ctx, time.Second)
			err := writeRPC(replyCtx, conn, rpcMessage{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage("null")})
			end()
			close(s.stop)
			if err != nil {
				return
			}
			continue
		}
		method, exists := s.methods[request.Method]
		if !exists {
			if replyRPCError(ctx, conn, request.ID, &RPCError{Code: -32601, Message: "method not found"}) != nil {
				return
			}
			continue
		}
		s.mu.Lock()
		var admissionError *RPCError
		if s.stopping {
			admissionError = stoppingRPCError()
		} else if s.active >= maxRPCRequests {
			admissionError = &RPCError{Code: -32001, Message: "Server overloaded; retry later"}
		}
		if admissionError != nil {
			s.mu.Unlock()
			if replyRPCError(ctx, conn, request.ID, admissionError) != nil {
				return
			}
			continue
		}
		s.active++
		s.calls.Add(1)
		s.mu.Unlock()
		requestCtx, cancelRequest := context.WithCancel(ctx)
		pendingMu.Lock()
		pending[key] = cancelRequest
		pendingMu.Unlock()
		go func() {
			defer func() {
				cancelRequest()
				pendingMu.Lock()
				delete(pending, key)
				pendingMu.Unlock()
				s.mu.Lock()
				s.active--
				s.mu.Unlock()
				s.calls.Done()
			}()
			result, failure := method.invoke(requestCtx, request.Params)
			replyCtx, end := context.WithTimeout(ctx, time.Second)
			defer end()
			response := rpcMessage{JSONRPC: "2.0", ID: request.ID, Result: result, Error: failure}
			payload, err := json.Marshal(response)
			if err != nil || len(payload) > handshakeReadLimit {
				response.Result = nil
				response.Error = &RPCError{Code: -32603, Message: "RPC result exceeds message limit or cannot be encoded"}
			}
			if writeRPC(replyCtx, conn, response) != nil {
				_ = conn.CloseNow()
			}
		}()
	}
}

func requestKey(id json.RawMessage) string {
	var text string
	if json.Unmarshal(id, &text) == nil {
		return "s:" + text
	}
	return "n:" + string(id)
}

func (m rpcMethod) invoke(ctx context.Context, params json.RawMessage) (result json.RawMessage, failure *RPCError) {
	defer func() {
		if recover() != nil {
			result = nil
			failure = &RPCError{Code: -32603, Message: "internal error"}
		}
	}()
	var object map[string]json.RawMessage
	if json.Unmarshal(params, &object) != nil || object == nil {
		return nil, &RPCError{Code: -32602, Message: "parameters must be a JSON object"}
	}
	input := reflect.New(m.input)
	if json.Unmarshal(params, input.Interface()) != nil {
		return nil, &RPCError{Code: -32602, Message: "invalid parameters"}
	}
	if ctx.Err() != nil {
		return nil, &RPCError{Code: -32800, Message: "request canceled"}
	}
	values := m.call.Call([]reflect.Value{reflect.ValueOf(ctx), input.Elem()})
	if !values[1].IsNil() {
		err := values[1].Interface().(error)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &RPCError{Code: -32800, Message: "request canceled"}
		}
		var failure *RPCError
		if errors.As(err, &failure) && failure != nil {
			return nil, failure
		}
		return nil, &RPCError{Code: -32000, Message: err.Error()}
	}
	result, err := json.Marshal(values[0].Interface())
	if err != nil {
		return nil, &RPCError{Code: -32603, Message: "result cannot be encoded"}
	}
	return result, nil
}

func stoppingRPCError() *RPCError {
	return &RPCError{Code: -32600, Message: "Server is draining; retry after reconnecting", Data: json.RawMessage(`{"reason":"serverShuttingDown"}`)}
}
