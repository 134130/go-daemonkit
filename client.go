package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"

	"github.com/coder/websocket"
)

// ErrClientClosed means the connection was closed. Calls are never replayed.
var ErrClientClosed = errors.New("daemon client closed")

// Client multiplexes calls over one connection. It is safe for concurrent use.
type Client struct {
	conn          *websocket.Conn
	ctx           context.Context
	cancel        context.CancelFunc
	writes        chan struct{}
	mu            sync.Mutex
	next          uint64
	pending       map[string]chan rpcMessage
	subscriptions map[string]*Subscription
	err           error
	done          chan struct{}
}

// Connect connects to an existing daemon. It never starts or replaces a process.
// ctx bounds connection setup; Close owns the connection lifetime after success.
func Connect(ctx context.Context, options Options) (*Client, error) {
	app, err := New(options)
	if err != nil {
		return nil, err
	}
	conn, _, err := connectLocal(ctx, app.endpoint, initializeParams{app.options.AppID, app.options.InstanceID, protocolVersion, app.options.Version})
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	client := &Client{conn: conn, ctx: lifetime, cancel: cancel, writes: make(chan struct{}, 1), pending: make(map[string]chan rpcMessage), done: make(chan struct{})}
	go client.read()
	return client, nil
}

// Call invokes an explicit RPC name with a JSON object and decodes the result into a
// non-nil pointer. Cancellation is cooperative and does not roll back side effects.
// Cancellation may wait for an in-flight write and best-effort cancel delivery;
// each transport write, including waiting for the writer, is bounded to five seconds.
func (c *Client) Call(ctx context.Context, method string, request, response any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validRPCMethod(method) {
		return fmt.Errorf("invalid or reserved RPC method name")
	}
	return c.call(ctx, method, request, response)
}

func (c *Client) call(ctx context.Context, method string, request, response any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target := reflect.ValueOf(response)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("response must be a non-nil pointer")
	}
	params, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(params) == 0 || params[0] != '{' {
		return fmt.Errorf("request must encode as a JSON object")
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	if len(c.pending) >= maxRPCRequests {
		c.mu.Unlock()
		return &RPCError{Code: -32001, Message: "too many pending calls"}
	}
	c.next++
	id := strconv.FormatUint(c.next, 10)
	replies := make(chan rpcMessage, 1)
	c.pending[id] = replies
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	rawID, _ := json.Marshal(id)
	if err := c.write(ctx, rpcMessage{JSONRPC: "2.0", ID: rawID, Method: method, Params: params}); err != nil {
		return err
	}
	select {
	case message := <-replies:
		if message.Error != nil {
			return message.Error
		}
		return json.Unmarshal(message.Result, response)
	case <-ctx.Done():
		cancelParams, _ := json.Marshal(struct {
			ID string `json:"id"`
		}{id})
		// Best effort: never use the canceled request context for a socket write.
		cancelCtx, end := context.WithTimeout(c.ctx, handshakeTimeout)
		defer end()
		_ = c.write(cancelCtx, rpcMessage{JSONRPC: "2.0", Method: cancelRPCMethod, Params: cancelParams})
		return ctx.Err()
	case <-c.ctx.Done():
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		return err
	}
}

func (c *Client) write(ctx context.Context, message rpcMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(payload) > handshakeReadLimit {
		return fmt.Errorf("RPC message exceeds %d bytes", handshakeReadLimit)
	}
	writeCtx, cancel := context.WithTimeout(c.ctx, handshakeTimeout)
	defer cancel()
	select {
	case c.writes <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-writeCtx.Done():
		return writeCtx.Err()
	}
	defer func() { <-c.writes }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.conn.Write(writeCtx, websocket.MessageText, payload); err != nil {
		c.fail(err)
		return err
	}
	return nil
}

func (c *Client) read() {
	defer close(c.done)
	for {
		message, err := readRPC(c.ctx, c.conn)
		if err != nil {
			c.fail(err)
			return
		}
		if message.Method == eventMethod {
			if err := c.receiveEvent(message); err != nil {
				c.fail(err)
				return
			}
			continue
		}
		var id string
		if message.Method != "" || len(message.Params) != 0 || json.Unmarshal(message.ID, &id) != nil || (message.Error == nil) == (len(message.Result) == 0) {
			c.fail(errors.New("invalid RPC response"))
			return
		}
		c.mu.Lock()
		if reply := c.pending[id]; reply != nil {
			// Only the first response belongs to this request; late canceled replies are ignored.
			delete(c.pending, id)
			reply <- message
		}
		c.mu.Unlock()
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
		c.cancel()
		for _, sub := range c.subscriptions {
			sub.queue.finish(err)
		}
		c.subscriptions = nil
	}
	c.mu.Unlock()
	_ = c.conn.CloseNow()
}

// Done closes when the connection's receive loop has exited, after pending calls
// and subscriptions have been failed. It reports observed disconnection, not a
// heartbeat or daemon health guarantee. Close also closes Done.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err returns the first connection failure, or nil until failure is observed.
// After Done closes it is always non-nil. Local Close uses ErrClientClosed unless
// a transport failure was already recorded. Calling Close does not erase that cause.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close fails pending calls and waits for the receive loop to stop.
func (c *Client) Close() error {
	c.fail(ErrClientClosed)
	<-c.done
	return nil
}
