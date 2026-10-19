package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestEventsDeliveryAndRPC(t *testing.T) {
	app, client, _ := testRPC(t)
	if err := app.Publish("clock.tick", 1); err != nil {
		t.Fatal(err)
	} // No replay.
	ctx, cancel := context.WithCancel(t.Context())
	sub, err := client.Subscribe(ctx, "clock.tick")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	cancel() // Registration context does not own the subscription.
	other, err := client.Subscribe(t.Context(), "clock.tick")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for i := range 8 {
		if err := app.Publish("clock.tick", rpcTestResponse{fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
		for _, s := range []*Subscription{sub, other} {
			wait, end := context.WithTimeout(t.Context(), 5*time.Second)
			var value rpcTestResponse
			err := s.Next(wait, &value)
			end()
			if err != nil || value.Value != fmt.Sprint(i) {
				t.Fatalf("event = %+v, %v", value, err)
			}
		}
	}
	var response rpcTestResponse
	if err := client.Call(t.Context(), "rpcTestService.Echo", rpcTestRequest{"rpc"}, &response); err != nil || response.Value != "rpc" {
		t.Fatalf("Call = %+v, %v", response, err)
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	// The server processes unsubscribe before this request on the same connection.
	if err := client.Call(t.Context(), "rpcTestService.Echo", rpcTestRequest{}, &response); err != nil {
		t.Fatal(err)
	}
	app.events.mu.Lock()
	count := len(app.events.subscriptions)
	app.events.mu.Unlock()
	if count != 1 {
		t.Fatalf("remaining subscriptions = %d", count)
	}
	if err := sub.Next(t.Context(), &response); !errors.Is(err, ErrSubscriptionClosed) {
		t.Fatalf("closed = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := other.Next(t.Context(), &response); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("disconnect = %v", err)
	}
}

func TestEventQueueCancellationOverflowAndDecode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sub := &Subscription{queue: newEventQueue()}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		var value int
		if err := sub.Next(ctx, &value); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait = %v", err)
		}
		sub.queue.push(json.RawMessage("7"))
		if err := sub.Next(t.Context(), nil); err == nil {
			t.Fatal("nil destination accepted")
		}
		if err := sub.Next(t.Context(), &value); err != nil || value != 7 {
			t.Fatalf("after cancellation = %d, %v", value, err)
		}
		sub.queue.push(json.RawMessage(`"wrong type"`))
		if err := sub.Next(t.Context(), &value); err == nil {
			t.Fatal("decode succeeded")
		}
		sub.queue.push(json.RawMessage("8"))
		if err := sub.Next(t.Context(), &value); err != nil || value != 8 {
			t.Fatalf("after decode = %d, %v", value, err)
		}
		for range eventBufferSize {
			if sub.queue.push(json.RawMessage("9")) {
				t.Fatal("early overflow")
			}
		}
		if !sub.queue.push(json.RawMessage("9")) {
			t.Fatal("overflow not detected")
		}
		if err := sub.Next(t.Context(), &value); !errors.Is(err, ErrEventOverflow) {
			t.Fatalf("overflow = %v", err)
		}
		if len(sub.queue.values) != 0 {
			t.Fatal("terminated queue retained values")
		}
	})
}

func TestPublishLimitsAndLifecycle(t *testing.T) {
	app := testApp(t)
	if err := app.Publish("clock.tick", nil); !errors.Is(err, ErrEventsClosed) {
		t.Fatal(err)
	}
	app.events.start()
	defer app.events.close()
	slow := &serverSubscription{id: "slow", topic: "slow", queue: newEventQueue()}
	healthy := &serverSubscription{id: "healthy", topic: "healthy", queue: newEventQueue()}
	app.events.subscriptions[slow] = struct{}{}
	app.events.subscriptions[healthy] = struct{}{}
	for range eventBufferSize + 1 {
		if err := app.Publish("slow", 1); err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(slow.queue.failure(), ErrEventOverflow) {
		t.Fatal("slow subscription stayed open")
	}
	if err := app.Publish("healthy", 2); err != nil {
		t.Fatal(err)
	}
	if data, err := healthy.queue.next(t.Context()); err != nil || string(data) != "2" {
		t.Fatalf("healthy = %s, %v", data, err)
	}
	for _, topic := range []string{"", "../bad", strings.Repeat("a", 65)} {
		if err := app.Publish(topic, 1); err == nil {
			t.Fatal("invalid topic accepted")
		}
	}
	if err := app.Publish("healthy", make(chan int)); err == nil {
		t.Fatal("unsupported value accepted")
	}
	if err := app.Publish("healthy", strings.Repeat("x", int(handshakeReadLimit))); err == nil {
		t.Fatal("oversize accepted")
	}
	app.events.close()
	if err := app.Publish("healthy", 1); !errors.Is(err, ErrEventsClosed) {
		t.Fatal(err)
	}
}

func TestSubscriptionsBoundedAndReusable(t *testing.T) {
	_, client, _ := testRPC(t)
	subs := make([]*Subscription, 0, maxSubscriptions)
	for range maxSubscriptions {
		sub, err := client.Subscribe(t.Context(), "topic")
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, sub)
	}
	if _, err := client.Subscribe(t.Context(), "topic"); err == nil {
		t.Fatal("subscription limit ignored")
	}
	for _, sub := range subs {
		if err := sub.Close(); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := client.Subscribe(t.Context(), "topic")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Subscribe(ctx, "topic"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled registration = %v", err)
	}
	if _, err := client.Subscribe(t.Context(), "../invalid"); err == nil {
		t.Fatal("invalid topic accepted")
	}
}

func TestClientEventOverflowDoesNotBreakRPC(t *testing.T) {
	_, client, _ := testRPC(t)
	sub, err := client.Subscribe(t.Context(), "topic")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	params, _ := json.Marshal(eventParams{Subscription: sub.id, Payload: json.RawMessage("1")})
	for range eventBufferSize + 1 {
		if err := client.receiveEvent(rpcMessage{JSONRPC: "2.0", Method: eventMethod, Params: params}); err != nil {
			t.Fatal(err)
		}
	}
	var value int
	if err := sub.Next(t.Context(), &value); !errors.Is(err, ErrEventOverflow) {
		t.Fatalf("overflow = %v", err)
	}
	var response rpcTestResponse
	if err := client.Call(t.Context(), "rpcTestService.Echo", rpcTestRequest{}, &response); err != nil {
		t.Fatal(err)
	}
}

func TestServerSubscriptionValidationAndLimits(t *testing.T) {
	app, client, _ := testRPC(t)
	var response struct{}
	for _, params := range []subscribeParams{{"one", "../bad"}, {"", "topic"}} {
		err := client.call(t.Context(), subscribeMethod, params, &response)
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != -32602 {
			t.Fatalf("validation = %v", err)
		}
	}
	for i := range maxSubscriptions {
		if err := client.call(t.Context(), subscribeMethod, subscribeParams{fmt.Sprint(i), "topic"}, &response); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		id   string
		code int
	}{{"0", -32602}, {"extra", -32001}} {
		err := client.call(t.Context(), subscribeMethod, subscribeParams{tc.id, "topic"}, &response)
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != tc.code {
			t.Fatalf("admission = %v", err)
		}
	}
	app.events.mu.Lock()
	queues := make([]*eventQueue, 0, len(app.events.subscriptions))
	for sub := range app.events.subscriptions {
		queues = append(queues, sub.queue)
	}
	app.events.mu.Unlock()
	_ = client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for _, q := range queues {
		select {
		case <-q.done:
		case <-ctx.Done():
			t.Fatal("disconnect did not close server subscription")
		}
	}
}

func eventTestClient(t *testing.T, conn *websocket.Conn) *Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{conn: conn, ctx: ctx, cancel: cancel, writes: make(chan struct{}, 1), pending: make(map[string]chan rpcMessage), done: make(chan struct{})}
	go c.read()
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestFirstEventAfterSubscribeReply(t *testing.T) {
	conn, done := connectTestServer(t, func(conn *websocket.Conn) error {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		request, err := readRPC(ctx, conn)
		if err != nil {
			return err
		}
		var registration subscribeParams
		if err := json.Unmarshal(request.Params, &registration); err != nil {
			return err
		}
		if err := writeRPC(ctx, conn, rpcMessage{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage("{}")}); err != nil {
			return err
		}
		params, _ := json.Marshal(eventParams{Subscription: registration.Subscription, Payload: json.RawMessage("42")})
		if err := writeRPC(ctx, conn, rpcMessage{JSONRPC: "2.0", Method: eventMethod, Params: params}); err != nil {
			return err
		}
		request, err = readRPC(ctx, conn)
		if err == nil && request.Method != unsubscribeMethod {
			return errors.New("expected unsubscribe")
		}
		return err
	})
	client := eventTestClient(t, conn)
	sub, err := client.Subscribe(t.Context(), "topic")
	if err != nil {
		t.Fatal(err)
	}
	var value int
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := sub.Next(ctx, &value); err != nil || value != 42 {
		t.Fatalf("first event = %d, %v", value, err)
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	if err := awaitHandshake(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledSubscriptionCleansLateRegistration(t *testing.T) {
	var hub eventHub
	hub.start()
	defer hub.close()
	entered := make(chan struct{})
	resume := make(chan struct{})
	conn, done := connectTestServer(t, func(conn *websocket.Conn) error {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		server := newRPCServer(nil)
		server.events = &hub
		session := newEventSession(ctx, conn, server)
		defer session.close()
		request, err := readRPC(ctx, conn)
		if err != nil {
			return err
		}
		close(entered)
		select {
		case <-resume:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := session.handle(request); err != nil {
			return err
		}
		for {
			request, err = readRPC(ctx, conn)
			if err != nil {
				return err
			}
			if request.Method == cancelRPCMethod {
				continue
			}
			if request.Method != unsubscribeMethod {
				return errors.New("expected unsubscribe")
			}
			return session.handle(request)
		}
	})
	client := eventTestClient(t, conn)
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { _, err := client.Subscribe(ctx, "topic"); result <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no registration")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("registration = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not cancel")
	}
	close(resume)
	if err := awaitHandshake(t, done); err != nil {
		t.Fatal(err)
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.subscriptions) != 0 || hub.active != 0 {
		t.Fatal("late registration leaked")
	}
}
