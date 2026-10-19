package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/coder/websocket"
)

type serverSubscription struct {
	id, topic string
	queue     *eventQueue
}
type eventSession struct {
	ctx           context.Context
	cancel        context.CancelFunc
	conn          *websocket.Conn
	server        *rpcServer
	mu            sync.Mutex
	subscriptions map[string]*serverSubscription
	workers       sync.WaitGroup
	active        int
}

func newEventSession(ctx context.Context, conn *websocket.Conn, server *rpcServer) *eventSession {
	ctx, cancel := context.WithCancel(ctx)
	return &eventSession{ctx: ctx, cancel: cancel, conn: conn, server: server, subscriptions: make(map[string]*serverSubscription)}
}
func (s *eventSession) remove(sub *serverSubscription) {
	s.mu.Lock()
	if s.subscriptions[sub.id] == sub {
		delete(s.subscriptions, sub.id)
	}
	s.mu.Unlock()
	if h := s.server.events; h != nil {
		h.mu.Lock()
		delete(h.subscriptions, sub)
		h.mu.Unlock()
	}
}

// Count retiring workers until they finish their bounded socket write.
func (s *eventSession) complete(sub *serverSubscription) {
	sub.queue.finish(ErrSubscriptionClosed)
	s.remove(sub)
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	h := s.server.events
	h.mu.Lock()
	h.active--
	h.mu.Unlock()
}
func (s *eventSession) close() {
	s.cancel()
	s.mu.Lock()
	for _, sub := range s.subscriptions {
		sub.queue.finish(ErrSubscriptionClosed)
	}
	s.mu.Unlock()
	s.workers.Wait()
}
func (s *eventSession) handle(request rpcMessage) error {
	var params subscribeParams
	if json.Unmarshal(request.Params, &params) != nil || !validName(params.Subscription) ||
		(request.Method == subscribeMethod && !validName(params.Topic)) {
		if len(request.ID) == 0 {
			return errors.New("invalid subscription notification")
		}
		return replyRPCError(s.ctx, s.conn, request.ID, &RPCError{Code: -32602, Message: "invalid subscription parameters"})
	}
	if request.Method == unsubscribeMethod {
		s.mu.Lock()
		sub := s.subscriptions[params.Subscription]
		s.mu.Unlock()
		if sub != nil {
			sub.queue.finish(ErrSubscriptionClosed)
			s.remove(sub)
		}
		if len(request.ID) == 0 {
			return nil
		}
		return s.reply(request.ID)
	}
	if len(request.ID) == 0 {
		return nil
	}
	s.server.mu.Lock()
	if s.server.stopping || s.server.events == nil {
		s.server.mu.Unlock()
		return replyRPCError(s.ctx, s.conn, request.ID, stoppingRPCError())
	}
	s.mu.Lock()
	h := s.server.events
	h.mu.Lock()
	var failure *RPCError
	if !h.running {
		failure = stoppingRPCError()
	} else if s.subscriptions[params.Subscription] != nil {
		failure = &RPCError{Code: -32602, Message: "duplicate subscription ID"}
	} else if s.active >= maxSubscriptions || h.active >= maxServerSubscriptions {
		failure = &RPCError{Code: -32001, Message: "too many subscriptions"}
	}
	var sub *serverSubscription
	if failure == nil {
		sub = &serverSubscription{id: params.Subscription, topic: params.Topic, queue: newEventQueue()}
		s.subscriptions[sub.id] = sub
		h.subscriptions[sub] = struct{}{}
		s.active++
		h.active++
	}
	h.mu.Unlock()
	s.mu.Unlock()
	s.server.mu.Unlock()
	if failure != nil {
		return replyRPCError(s.ctx, s.conn, request.ID, failure)
	}
	// Queue publications immediately, but never send an event before the registration reply.
	if err := s.reply(request.ID); err != nil {
		s.complete(sub)
		return err
	}
	s.workers.Go(func() { s.pump(sub) })
	return nil
}
func (s *eventSession) reply(id json.RawMessage) error {
	ctx, cancel := context.WithTimeout(s.ctx, handshakeTimeout)
	defer cancel()
	return writeRPC(ctx, s.conn, rpcMessage{JSONRPC: "2.0", ID: id, Result: json.RawMessage("{}")})
}
func (s *eventSession) pump(sub *serverSubscription) {
	defer s.complete(sub)
	for {
		value, err := sub.queue.next(s.ctx)
		params := eventParams{Subscription: sub.id, Payload: value}
		if err != nil {
			if !errors.Is(err, ErrEventOverflow) {
				return
			}
			params.Error = "overflow"
		}
		encoded, _ := json.Marshal(params)
		ctx, cancel := context.WithTimeout(s.ctx, handshakeTimeout)
		writeErr := writeRPC(ctx, s.conn, rpcMessage{JSONRPC: "2.0", Method: eventMethod, Params: encoded})
		cancel()
		if writeErr != nil {
			_ = s.conn.CloseNow()
			return
		}
		if err != nil {
			return
		}
	}
}
