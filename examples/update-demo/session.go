package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	daemon "github.com/134130/go-daemonkit"
	"github.com/134130/go-daemonkit/examples/update-demo/api"
)

type action string

const (
	connectAction action = "connect"
	updateAction  action = "update"
	nowAction     action = "demo.now"
	rawAction     action = "demo.now (raw)"
	greetAction   action = "demo.greet"
	runtimeAction action = "demo.runtime"
	watchAction   action = "tick event"
	stopAction    action = "stop"
)

type resultMsg struct {
	action    action
	info      api.InfoResponse
	connected bool
	detail    string
	err       error
}

// Commands are serialized with Close; only the Tea update loop owns UI state.
type session struct {
	mu      sync.Mutex
	manager *daemon.Manager
	options daemon.Options
	client  *daemon.Client
}

func (s *session) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disconnect()
}
func (s *session) disconnect() {
	if s.client != nil {
		_ = s.client.Close()
		s.client = nil
	}
}
func (s *session) connect(ctx context.Context) error {
	s.disconnect()
	client, err := daemon.Connect(ctx, s.options)
	if err != nil {
		return err
	}
	s.client = client
	return nil
}
func (s *session) execute(parent context.Context, action action, candidate string) resultMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	result := resultMsg{action: action}
	if err := ctx.Err(); err != nil {
		result.err = err
		return result
	}
	switch action {
	case connectAction:
		result.err = s.manager.EnsureRunning(ctx)
		if result.err == nil {
			result.err = s.connect(ctx)
		}
		result.detail = "Connected to the selected daemon release."
	case updateAction:
		if candidate == "" {
			result.err = errors.New("select an available build")
			break
		}
		s.disconnect()
		result.err = s.manager.Update(ctx, candidate)
		if result.err == nil {
			result.err = s.manager.EnsureRunning(ctx)
		}
		// Reconnect after success or rollback. Never replay the interrupted RPC.
		result.err = errors.Join(result.err, s.connect(ctx))
		result.detail = "Update finished; reconnected to the daemon."
	case stopAction:
		s.disconnect()
		result.err = s.manager.Stop(ctx)
		result.detail = "Daemon stopped. Press c to start its selected release."
	case nowAction, rawAction, greetAction, runtimeAction, watchAction:
		if s.client == nil {
			result.err = errors.New("not connected; press c")
			break
		}
		callCtx, end := context.WithTimeout(ctx, 5*time.Second)
		defer end()
		if action == runtimeAction {
			response, err := s.client.Invoke(callCtx, api.Runtime, api.Empty{})
			result.err = err
			result.detail = fmt.Sprintf("%s · %s/%s", response.GoVersion, response.OS, response.Arch)
		} else if action == watchAction {
			sub, err := s.client.SubscribeTopic(callCtx, api.Tick)
			result.err = err
			if err == nil {
				defer sub.Close()
				eventCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
				defer cancel()
				tick, err := sub.Next(eventCtx)
				result.err = err
				result.detail = tick.Time.Format(time.RFC3339)
			}
		} else if action == greetAction {
			var response api.GreetResponse
			response, result.err = s.client.Invoke(callCtx, api.Greet, api.GreetRequest{Name: "old CLI"})
			result.detail = response.Message
		} else {
			var response api.NowResponse
			if action == rawAction {
				result.err = s.client.Call(callCtx, "demo.now", api.Empty{}, &response)
			} else {
				response, result.err = s.client.Invoke(callCtx, api.Now, api.Empty{})
			}
			result.detail = fmt.Sprintf("v%s · %s", response.Version, response.Time.Format(time.RFC3339))
		}
	}
	if s.client != nil {
		infoCtx, end := context.WithTimeout(ctx, 5*time.Second)
		info, err := s.client.Invoke(infoCtx, api.Info, api.Empty{})
		end()
		if err != nil {
			s.disconnect()
			result.err = errors.Join(result.err, err)
		} else {
			result.info, result.connected = info, true
		}
	}
	return result
}
