package config

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrNotRunning = errors.New("config service is not running")

// Prepared holds an application's replacement state. Commit owns atomic
// publication and must honor ctx; an error does not imply side effects rolled back.
// Discard releases uncommitted resources, including on Prepare/Commit failure.
// Successful Commit transfers ownership to the application (Discard is not called).
type Prepared struct {
	Commit  func(context.Context) error
	Discard func()
}

// ReloadStatus is a process-local snapshot, separate from file contents.
// Revision advances only after successful Commit, including the initial Startup.
// LastError comes from application callbacks and must be safe to display.
type ReloadStatus struct {
	Revision    uint64
	Attempts    uint64
	LastAttempt time.Time
	LastError   error
}

// Service is an optional lifecycle participant: app.Register(service) suffices.
// Load/Prepare/Commit are serialized. Do not call Reload/Startup/Shutdown from
// callbacks. Status is safe from callbacks. Applied values belong to the app and
// must be treated as immutable; this service keeps no copy of live configuration.
type Service[T any] struct {
	file     *File[T]
	prepare  func(context.Context, T) (Prepared, error)
	gate     chan struct{}
	lifetime context.Context
	stop     context.CancelFunc
	mu       sync.Mutex
	started  bool
	closed   bool
	status   ReloadStatus
}

func NewService[T any](file *File[T], prepare func(context.Context, T) (Prepared, error)) (*Service[T], error) {
	if file == nil || prepare == nil {
		return nil, errors.New("config file and prepare function are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service[T]{file: file, prepare: prepare, gate: make(chan struct{}, 1), lifetime: ctx, stop: cancel}, nil
}

func (s *Service[T]) Status() ReloadStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Service[T]) Startup(ctx context.Context) error { return s.apply(ctx, true) }
func (s *Service[T]) Reload(ctx context.Context) error  { return s.apply(ctx, false) }

func (s *Service[T]) apply(parent context.Context, startup bool) (err error) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(s.lifetime, cancel)
	defer func() { stop(); cancel() }()
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.gate }()
	s.mu.Lock()
	if s.closed || (startup && s.started) || (!startup && !s.started) {
		s.mu.Unlock()
		return ErrNotRunning
	}
	s.status.Attempts++
	s.status.LastAttempt = time.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.status.LastError = err
		if err == nil {
			s.status.Revision++
			s.started = true
		}
		if startup && err != nil {
			s.closed = true
			s.stop()
		}
		s.mu.Unlock()
	}()
	value, err := s.file.Load(ctx)
	if err != nil {
		return err
	}
	prepared, err := s.prepare(ctx, value)
	committed := false
	defer func() {
		if !committed && prepared.Discard != nil {
			prepared.Discard()
		}
	}()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if prepared.Commit == nil {
		return errors.New("prepared config has no commit function")
	}
	if err := prepared.Commit(ctx); err != nil {
		return err
	}
	// Cancellation after publication cannot undo Commit or hide its success.
	committed = true
	return nil
}

// Shutdown closes admission, cancels active preparation, and waits for it to
// unwind. A Commit already in progress may complete. Callbacks must cooperate;
// timeout leaves admission closed. Shutdown does not destroy app-owned state.
func (s *Service[T]) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.stop()
	s.mu.Unlock()
	select {
	case s.gate <- struct{}{}:
		<-s.gate
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
