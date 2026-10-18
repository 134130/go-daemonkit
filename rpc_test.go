package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type rpcTestRequest struct {
	Value string `json:"value"`
}
type rpcTestResponse struct {
	Value string `json:"value"`
}
type rpcBadResponse struct {
	Unsupported chan int `json:"unsupported"`
}
type rpcTestService struct {
	entered  chan string
	canceled chan string
	release  chan struct{}
}

func (*rpcTestService) Echo(_ context.Context, req rpcTestRequest) (rpcTestResponse, error) {
	return rpcTestResponse{req.Value}, nil
}
func (s *rpcTestService) Wait(ctx context.Context, req rpcTestRequest) (rpcTestResponse, error) {
	s.entered <- req.Value
	select {
	case <-s.release:
		return rpcTestResponse{req.Value}, nil
	case <-ctx.Done():
		s.canceled <- req.Value
		return rpcTestResponse{}, ctx.Err()
	}
}
func (*rpcTestService) Fail(context.Context, rpcTestRequest) (rpcTestResponse, error) {
	return rpcTestResponse{}, errors.New("application failure")
}
func (*rpcTestService) Panic(context.Context, rpcTestRequest) (rpcTestResponse, error) {
	panic("private panic detail")
}
func (*rpcTestService) BadResult(context.Context, rpcTestRequest) (rpcBadResponse, error) {
	return rpcBadResponse{}, nil
}

func testRPC(t *testing.T) (*App, *Client, *rpcTestService) {
	t.Helper()
	a := testApp(t)
	service := &rpcTestService{entered: make(chan string, 128), canceled: make(chan string, 128), release: make(chan struct{})}
	if err := a.Register(service); err != nil {
		t.Fatal(err)
	}
	runTestApp(t, a)
	connectTestApp(t, a)
	client, err := Connect(t.Context(), a.options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return a, client, service
}

func receiveRPCSignal(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("RPC signal timed out")
		return ""
	}
}

func TestRPCConcurrentCallsAndCancellation(t *testing.T) {
	_, client, service := testRPC(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	slow := make(chan error, 1)
	go func() {
		var result rpcTestResponse
		slow <- client.Call(ctx, "rpcTestService.Wait", rpcTestRequest{"slow"}, &result)
	}()
	receiveRPCSignal(t, service.entered)
	var callers sync.WaitGroup
	for i := range 20 {
		callers.Go(func() {
			value := fmt.Sprintf("call-%d", i)
			var result rpcTestResponse
			if err := client.Call(t.Context(), "rpcTestService.Echo", rpcTestRequest{value}, &result); err != nil || result.Value != value {
				t.Errorf("Echo = %+v, %v", result, err)
			}
		})
	}
	callers.Wait()
	cancel()
	if err := <-slow; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	if value := receiveRPCSignal(t, service.canceled); value != "slow" {
		t.Fatal(value)
	}
	var result rpcTestResponse
	if err := client.Call(t.Context(), "rpcTestService.Echo", rpcTestRequest{"still connected"}, &result); err != nil {
		t.Fatal(err)
	}
}

func TestRPCFailuresDoNotBreakConnection(t *testing.T) {
	_, client, _ := testRPC(t)
	for _, test := range []struct {
		method string
		params any
		code   int
	}{
		{"Fail", rpcTestRequest{}, -32000},
		{"Panic", rpcTestRequest{}, -32603},
		{"BadResult", rpcTestRequest{}, -32603},
		{"Unknown", rpcTestRequest{}, -32601},
		{"Startup", rpcTestRequest{}, -32601},
		{"Echo", map[string]any{"value": 3}, -32602},
	} {
		var result rpcTestResponse
		err := client.Call(t.Context(), "rpcTestService."+test.method, test.params, &result)
		var failure *RPCError
		if !errors.As(err, &failure) || failure.Code != test.code {
			t.Fatalf("%s = %v", test.method, err)
		}
		if strings.Contains(err.Error(), "private panic detail") {
			t.Fatal("panic leaked")
		}
	}
	var result rpcTestResponse
	if err := client.Call(t.Context(), "rpcTestService.Echo", rpcTestRequest{"ok"}, &result); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(t.Context(), "rpcTestService.Echo", rpcTestRequest{strings.Repeat("x", handshakeReadLimit)}, &result); err == nil {
		t.Fatal("oversized request accepted")
	}
	if err := client.Call(t.Context(), "rpcTestService.Echo", rpcTestRequest{}, nil); err == nil {
		t.Fatal("nil response accepted")
	}
}

func TestRPCDisconnectCancelsWork(t *testing.T) {
	_, client, service := testRPC(t)
	result := make(chan error, 1)
	go func() {
		var response rpcTestResponse
		result <- client.Call(t.Context(), "rpcTestService.Wait", rpcTestRequest{"disconnect"}, &response)
	}()
	receiveRPCSignal(t, service.entered)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrClientClosed) {
		t.Fatalf("Call = %v", err)
	}
	receiveRPCSignal(t, service.canceled)
}

func TestRPCShutdownDrainsWork(t *testing.T) {
	a, client, service := testRPC(t)
	result := make(chan error, 1)
	go func() {
		var response rpcTestResponse
		result <- client.Call(t.Context(), "rpcTestService.Wait", rpcTestRequest{"drain"}, &response)
	}()
	receiveRPCSignal(t, service.entered)
	conn, _ := connectTestApp(t, a)
	if err := writeRPC(t.Context(), conn, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`"stop"`), Method: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	if _, err := readRPC(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	var response rpcTestResponse
	err := client.Call(t.Context(), "rpcTestService.Echo", rpcTestRequest{}, &response)
	var failure *RPCError
	if !errors.As(err, &failure) || failure.Code != -32600 || string(failure.Data) != `{"reason":"serverShuttingDown"}` {
		t.Fatalf("call during drain = %v", err)
	}
	select {
	case <-service.canceled:
		t.Fatal("active call canceled before grace")
	default:
	}
	close(service.release)
	if err := <-result; err != nil {
		t.Fatalf("drained request = %v", err)
	}
}

func TestRPCCancelRespondsToOriginalRequest(t *testing.T) {
	a, _, service := testRPC(t)
	conn, _ := connectTestApp(t, a)
	request := rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`"work"`), Method: "rpcTestService.Wait", Params: json.RawMessage(`{"value":"wire"}`)}
	if err := writeRPC(t.Context(), conn, request); err != nil {
		t.Fatal(err)
	}
	receiveRPCSignal(t, service.entered)
	if err := writeRPC(t.Context(), conn, rpcMessage{JSONRPC: "2.0", Method: "$/cancelRequest", Params: json.RawMessage(`{"id":"work"}`)}); err != nil {
		t.Fatal(err)
	}
	response, err := readRPC(t.Context(), conn)
	if err != nil {
		t.Fatal(err)
	}
	if string(response.ID) != `"work"` || response.Error == nil || response.Error.Code != -32800 {
		t.Fatalf("cancel reply = %+v", response)
	}
}

func TestRPCOverloadRejectsWithoutQueueing(t *testing.T) {
	a, client, service := testRPC(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	results := make(chan error, maxRPCRequests)
	for i := range maxRPCRequests {
		go func() {
			var response rpcTestResponse
			results <- client.Call(ctx, "rpcTestService.Wait", rpcTestRequest{fmt.Sprint(i)}, &response)
		}()
	}
	for range maxRPCRequests {
		receiveRPCSignal(t, service.entered)
	}
	second, err := Connect(t.Context(), a.options)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var response rpcTestResponse
	err = second.Call(t.Context(), "rpcTestService.Echo", rpcTestRequest{}, &response)
	var failure *RPCError
	if !errors.As(err, &failure) || failure.Code != -32001 {
		t.Fatalf("overload = %v", err)
	}
	cancel()
	for range maxRPCRequests {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Errorf("cancel = %v", err)
		}
		receiveRPCSignal(t, service.canceled)
	}
}

func (*rpcTestService) Denied(context.Context, rpcTestRequest) (rpcTestResponse, error) {
	return rpcTestResponse{}, &RPCError{Code: 1001, Message: "Access denied", Data: json.RawMessage(`{"reason":"readOnly"}`)}
}

func TestRPCStructuredApplicationError(t *testing.T) {
	_, client, _ := testRPC(t)
	var result rpcTestResponse
	err := client.Call(t.Context(), "rpcTestService.Denied", rpcTestRequest{}, &result)
	var failure *RPCError
	if !errors.As(err, &failure) || failure.Code != 1001 || string(failure.Data) != `{"reason":"readOnly"}` {
		t.Fatalf("application error = %v", err)
	}
}

func TestRPCShutdownCancelsAfterGrace(t *testing.T) {
	a, client, service := testRPC(t)
	result := make(chan error, 1)
	go func() {
		var response rpcTestResponse
		result <- client.Call(t.Context(), "rpcTestService.Wait", rpcTestRequest{"grace"}, &response)
	}()
	receiveRPCSignal(t, service.entered)
	conn, _ := connectTestApp(t, a)
	if err := writeRPC(t.Context(), conn, rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`"stop"`), Method: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	if _, err := readRPC(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	if value := receiveRPCSignal(t, service.canceled); value != "grace" {
		t.Fatal(value)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("unfinished request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("pending call survived daemon shutdown")
	}
}
