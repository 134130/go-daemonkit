package daemon

import (
	"context"
	"errors"
	"testing"
)

// A handler need not be a service method or belong to a registered service.
func TestTypedMethod(t *testing.T) {
	a := testApp(t)
	service := &rpcOnlyService{}
	if err := a.Register(service); err != nil {
		t.Fatal(err)
	}
	method := NewMethod[serviceRequest, serviceResponse]("workspace.lookup")
	if err := a.Handle(method, func(_ context.Context, request serviceRequest) (serviceResponse, error) {
		return serviceResponse{Value: request.Path}, nil
	}); err != nil {
		t.Fatal(err)
	}
	// The same DTOs can serve another independent operation.
	other := NewMethod[serviceRequest, serviceResponse]("workspace.other")
	if err := a.Handle(other, service.List); err != nil {
		t.Fatal(err)
	}
	cancel, done := runTestApp(t, a)
	connectTestApp(t, a)
	client, err := Connect(t.Context(), a.options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	result, err := client.Invoke(t.Context(), method, serviceRequest{Path: "hello"})
	if err != nil || result.Value != "hello" {
		t.Fatalf("Invoke = %+v, %v", result, err)
	}
	result, err = client.Invoke(t.Context(), other, serviceRequest{})
	if err != nil || result != (serviceResponse{}) {
		t.Fatalf("second contract = %+v, %v", result, err)
	}
	var response serviceResponse
	err = client.Call(t.Context(), "rpcOnlyService.List", serviceRequest{}, &response)
	var failure *RPCError
	if !errors.As(err, &failure) || failure.Code != -32601 {
		t.Fatalf("unexposed method = %v", err)
	}
	if err := a.Handle(NewMethod[serviceRequest, serviceResponse]("late"), service.List); err == nil {
		t.Fatal("registered during Run")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := a.Handle(NewMethod[serviceRequest, serviceResponse]("late"), service.List); err == nil {
		t.Fatal("registered after Run")
	}
}

func TestMethodValidation(t *testing.T) {
	a := testApp(t)
	handler := (&rpcOnlyService{}).List
	for _, name := range []string{"", " ", " clock.now", "initialize", "initialized", "shutdown", "$/subscribe", "$/cancelRequest", "rpc.discover"} {
		method := NewMethod[serviceRequest, serviceResponse](name)
		if err := a.Handle(method, handler); err == nil {
			t.Errorf("accepted name %q", name)
		}
		// Invalid contracts must fail before touching the connection.
		if _, err := (*Client)(nil).Invoke(t.Context(), method, serviceRequest{}); err == nil {
			t.Errorf("invoked invalid name %q", name)
		}
	}
	if err := a.Handle(Method[serviceRequest, serviceResponse]{}, handler); err == nil {
		t.Fatal("accepted zero contract")
	}
	if err := a.Handle(NewMethod[serviceRequest, serviceResponse]("nil"), nil); err == nil {
		t.Fatal("accepted nil handler")
	}
	if err := a.Handle(NewMethod[*serviceRequest, serviceResponse]("pointer"), (&pointerRequestService{}).List); err == nil {
		t.Fatal("accepted pointer DTO")
	}
	if err := a.Handle(NewMethod[serviceRequest, string]("string"), func(context.Context, serviceRequest) (string, error) {
		return "", nil
	}); err == nil {
		t.Fatal("accepted non-struct DTO")
	}
	if len(a.methods) != 0 {
		t.Fatal("invalid registration changed registry")
	}
	method := NewMethod[serviceRequest, serviceResponse]("stable")
	if err := a.Handle(method, handler); err != nil {
		t.Fatal(err)
	}
	if err := a.Handle(method, handler); err == nil {
		t.Fatal("accepted duplicate wire name")
	}
}

func TestInvokeErrorsAndCancellation(t *testing.T) {
	_, client, service := testRPC(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := client.Invoke(ctx, NewMethod[rpcTestRequest, rpcTestResponse]("rpcTestService.Wait"), rpcTestRequest{Value: "typed"})
		done <- err
	}()
	receiveRPCSignal(t, service.entered)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Invoke cancellation = %v", err)
	}
	receiveRPCSignal(t, service.canceled)
	result, err := client.Invoke(t.Context(), NewMethod[rpcTestRequest, rpcTestResponse]("rpcTestService.Denied"), rpcTestRequest{})
	var failure *RPCError
	if !errors.As(err, &failure) || failure.Code != 1001 || result != (rpcTestResponse{}) {
		t.Fatalf("Invoke failure = %+v, %v", result, err)
	}
}
