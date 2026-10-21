package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/134130/go-daemonkit/examples/foreground/clockapi"
)

// ClockService owns its background worker from Startup through Shutdown.
type ClockService struct {
	publish func(clockapi.NowResponse) error
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func (s *ClockService) Startup(ctx context.Context) error {
	// Startup's context ends when the hook returns; the worker lives until Shutdown.
	workerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.wg.Go(func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case now := <-ticker.C:
				log.Printf("clock: %s", now.Format(time.RFC3339))
				if s.publish != nil {
					if err := s.publish(clockapi.NowResponse{Time: now}); err != nil {
						log.Printf("clock publish: %v", err)
					}
				}
			}
		}
	})
	return nil
}

func (s *ClockService) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.cancel()
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		log.Print("clock: stopped")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*ClockService) Now(ctx context.Context, _ clockapi.NowRequest) (clockapi.NowResponse, error) {
	log.Print("clock: Now called")
	for range 10 {
		select {
		case <-ctx.Done():
			log.Print("clock: Now canceled")
			return clockapi.NowResponse{}, ctx.Err()
		default:
			time.Sleep(100 * time.Millisecond) // Simulate work
		}
	}
	log.Print("clock: Now returning time")
	return clockapi.NowResponse{Time: time.Now()}, nil
}
