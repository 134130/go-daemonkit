package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Method binds a stable wire name to request and response DTOs.
// Names are independent of Go package, type, and handler names.
// JSON schema compatibility belongs to the application.
type Method[Req, Resp any] struct {
	name string
}

// NewMethod declares a shared RPC contract without registering or calling it.
// Handle and Invoke validate the name and require named struct DTO values.
// The zero Method is invalid.
func NewMethod[Req, Resp any](name string) Method[Req, Resp] {
	return Method[Req, Resp]{name: name}
}

func validRPCMethod(name string) bool {
	return name != "" && strings.TrimSpace(name) == name &&
		!strings.HasPrefix(name, "rpc.") && !strings.HasPrefix(name, "$/") &&
		name != "initialize" && name != "initialized" && name != "shutdown"
}

func namedStruct(typ reflect.Type) bool {
	return typ.Kind() == reflect.Struct && typ.Name() != ""
}

func (m Method[Req, Resp]) validate() error {
	if !validRPCMethod(m.name) {
		return fmt.Errorf("invalid or reserved RPC method name")
	}
	if !namedStruct(reflect.TypeFor[Req]()) || !namedStruct(reflect.TypeFor[Resp]()) {
		return fmt.Errorf("RPC request and response must be named struct values")
	}
	return nil
}

// Handle exposes only handler under method's wire name. Its signature must match
// the contract. Handlers may run concurrently and must observe cancellation.
// Handle does not register lifecycle hooks; use Register for their owner.
// Duplicate names and registration after Run begins are rejected.
func (a *App) Handle[Req, Resp any](method Method[Req, Resp], handler func(context.Context, Req) (Resp, error)) error {
	a.serviceMu.Lock()
	defer a.serviceMu.Unlock()
	if a.sealed {
		return fmt.Errorf("RPC registration is closed")
	}
	if err := method.validate(); err != nil {
		return err
	}
	if handler == nil {
		return fmt.Errorf("RPC handler must not be nil")
	}
	if _, exists := a.methods[method.name]; exists {
		return fmt.Errorf("RPC method %s is already registered", method.name)
	}
	if a.methods == nil {
		a.methods = make(map[string]rpcMethod)
	}
	a.methods[method.name] = rpcMethod{call: func(ctx context.Context, params json.RawMessage) (any, error) {
		var request Req
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, &RPCError{Code: -32602, Message: "invalid parameters"}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return handler(ctx, request)
	}}
	return nil
}

// Invoke calls a shared contract using Call's cancellation and concurrency rules.
// On failure it returns the response's zero value and the error.
func (c *Client) Invoke[Req, Resp any](ctx context.Context, method Method[Req, Resp], request Req) (Resp, error) {
	var response Resp
	if err := method.validate(); err != nil {
		return response, err
	}
	if err := c.Call(ctx, method.name, request, &response); err != nil {
		var zero Resp
		return zero, err
	}
	return response, nil
}
