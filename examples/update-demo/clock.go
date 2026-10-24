package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/134130/go-daemonkit/examples/update-demo/api"
)

// ClockService owns its background worker from Startup through Shutdown.
type ClockService struct {
	logger  *log.Logger
	publish func(api.NowResponse) error
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
				s.logger.Printf("clock: %s", now.Format(time.RFC3339))
				if s.publish != nil {
					if err := s.publish(api.NowResponse{Time: now}); err != nil {
						s.logger.Printf("clock publish: %v", err)
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
		s.logger.Print("clock: stopped")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
