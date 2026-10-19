package daemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

const (
	subscribeMethod        = "$/subscribe"
	unsubscribeMethod      = "$/unsubscribe"
	eventMethod            = "$/event"
	maxSubscriptions       = 32
	maxServerSubscriptions = 256
	eventBufferSize        = 16
)

var (
	ErrEventsClosed       = errors.New("daemon events are not running")
	ErrSubscriptionClosed = errors.New("event subscription closed")
	ErrEventOverflow      = errors.New("event subscription buffer exceeded")
)

type eventParams struct {
	Subscription string          `json:"subscription"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Error        string          `json:"error,omitempty"`
}
type subscribeParams struct {
	Subscription string `json:"subscription"`
	Topic        string `json:"topic"`
}

type eventQueue struct {
	mu     sync.Mutex
	values chan json.RawMessage
	done   chan struct{}
	err    error
}

func newEventQueue() *eventQueue {
	return &eventQueue{values: make(chan json.RawMessage, eventBufferSize), done: make(chan struct{})}
}

func (q *eventQueue) finish(err error) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.finishLocked(err)
}
func (q *eventQueue) finishLocked(err error) bool {
	if q.err != nil {
		return false
	}
	q.err = err
	close(q.done)
	// Termination discards queued values rather than silently resuming after a gap.
	for len(q.values) > 0 {
		select {
		case <-q.values:
		default:
		}
	}
	return true
}
func (q *eventQueue) push(value json.RawMessage) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return false
	}
	select {
	case q.values <- value:
		return false
	default:
		return q.finishLocked(ErrEventOverflow)
	}
}
func (q *eventQueue) failure() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.err
}
func (q *eventQueue) next(ctx context.Context) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := q.failure(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-q.done:
		return nil, q.failure()
	case value := <-q.values:
		if err := q.failure(); err != nil {
			return nil, err
		}
		return value, nil
	}
}

// Subscription receives events in publication order. Call Next sequentially.
// Closing or failing the subscription discards queued events; there is no replay.
type Subscription struct {
	client    *Client
	id        string
	queue     *eventQueue
	closeOnce sync.Once
	closeErr  error
}

// Subscribe registers an exact topic name. ctx bounds registration only.
// The subscription lives until Close or connection termination. Topic names use
// the same path-safe identifier syntax as namespaces (dots are allowed).
func (c *Client) Subscribe(ctx context.Context, topic string) (*Subscription, error) {
	if !validName(topic) {
		return nil, fmt.Errorf("invalid event topic")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sub := &Subscription{client: c, id: rand.Text(), queue: newEventQueue()}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	if len(c.subscriptions) >= maxSubscriptions {
		c.mu.Unlock()
		return nil, &RPCError{Code: -32001, Message: "too many subscriptions"}
	}
	if c.subscriptions == nil {
		c.subscriptions = make(map[string]*Subscription)
	}
	c.subscriptions[sub.id] = sub
	c.mu.Unlock()
	var result struct{}
	if err := c.call(ctx, subscribeMethod, subscribeParams{sub.id, topic}, &result); err != nil {
		_ = sub.Close()
		return nil, err
	}
	return sub, nil
}

// Next consumes one event and decodes it into a non-nil pointer, as Call does.
// Invalid destinations do not consume an event. A decode failure consumes that
// event but leaves the subscription active. Canceling ctx only cancels this wait.
func (s *Subscription) Next(ctx context.Context, dst any) error {
	target := reflect.ValueOf(dst)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("event destination must be a non-nil pointer")
	}
	payload, err := s.queue.next(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, dst)
}

// Close releases the local subscription and sends an unsubscribe notification.
// It is idempotent. The bounded write does not wait for a server acknowledgment.
// If unsubscribe cannot be sent, the connection is closed to release server resources.
func (s *Subscription) Close() error {
	s.closeOnce.Do(func() {
		s.queue.finish(ErrSubscriptionClosed)
		s.client.mu.Lock()
		delete(s.client.subscriptions, s.id)
		s.client.mu.Unlock()
		params, _ := json.Marshal(subscribeParams{Subscription: s.id})
		s.closeErr = s.client.write(s.client.ctx, rpcMessage{JSONRPC: "2.0", Method: unsubscribeMethod, Params: params})
		if s.closeErr != nil {
			// Closing the connection also releases registrations when unsubscribe cannot be sent.
			s.client.fail(s.closeErr)
		}
	})
	return s.closeErr
}

// receiveEvent runs on the shared reader and must never wait for a consumer or write.
func (c *Client) receiveEvent(message rpcMessage) error {
	if len(message.ID) != 0 || message.Error != nil || len(message.Result) != 0 {
		return errors.New("invalid event notification")
	}
	var params eventParams
	if json.Unmarshal(message.Params, &params) != nil || !validName(params.Subscription) ||
		(params.Error == "" && len(params.Payload) == 0) || (params.Error != "" && (params.Error != "overflow" || len(params.Payload) != 0)) {
		return errors.New("invalid event notification")
	}
	c.mu.Lock()
	sub := c.subscriptions[params.Subscription]
	if sub != nil && params.Error != "" {
		delete(c.subscriptions, params.Subscription)
	}
	c.mu.Unlock()
	if sub == nil {
		return nil
	} // Unsubscribe may race with already-written notifications.
	if params.Error != "" {
		sub.queue.finish(ErrEventOverflow)
		return nil
	}
	if sub.queue.push(params.Payload) {
		go func() { _ = sub.Close() }()
	}
	return nil
}

type eventHub struct {
	mu            sync.Mutex
	running       bool
	active        int
	subscriptions map[*serverSubscription]struct{}
}

func (h *eventHub) start() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running = true
	h.active = 0
	h.subscriptions = make(map[*serverSubscription]struct{})
}
func (h *eventHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running = false
	for sub := range h.subscriptions {
		sub.queue.finish(ErrEventsClosed)
	}
	h.subscriptions = nil
}

// Publish encodes and enqueues an event without waiting for consumers.
// It succeeds with no subscribers. Slow subscribers terminate with ErrEventOverflow.
// Events are available during Service Startup through Shutdown; outside Run,
// Publish returns ErrEventsClosed. Payloads must fit the 64 KiB wire message limit.
func (a *App) Publish(topic string, payload any) error {
	if !validName(topic) {
		return fmt.Errorf("invalid event topic")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// Reserve the full envelope size, including the longest permitted subscription ID.
	params, _ := json.Marshal(eventParams{Subscription: strings.Repeat("x", 64), Payload: data})
	envelope, _ := json.Marshal(rpcMessage{JSONRPC: "2.0", Method: eventMethod, Params: params})
	if len(envelope) > handshakeReadLimit {
		return fmt.Errorf("event exceeds message limit")
	}
	h := &a.events
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.running {
		return ErrEventsClosed
	}
	for sub := range h.subscriptions {
		if sub.topic == topic && sub.queue.push(data) {
			delete(h.subscriptions, sub)
		}
	}
	return nil
}
