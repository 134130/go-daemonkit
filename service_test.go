package daemon

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type serviceRequest struct {
	Path string `json:"path"`
}
type serviceResponse struct {
	Value string `json:"value"`
}
type rpcOnlyService struct{}

func (*rpcOnlyService) List(context.Context, serviceRequest) (serviceResponse, error) {
	return serviceResponse{}, nil
}

type badRPCService struct{}

func (*badRPCService) List(string) error { return nil }

type badHookService struct{}

func (*badHookService) Startup() error { return nil }

type pointerRequestService struct{}

func (*pointerRequestService) List(context.Context, *serviceRequest) (serviceResponse, error) {
	return serviceResponse{}, nil
}

type testHooks struct {
	start func(context.Context) error
	stop  func(context.Context) error
}

func (s *testHooks) Startup(ctx context.Context) error {
	if s.start != nil {
		return s.start(ctx)
	}
	return nil
}
func (s *testHooks) Shutdown(ctx context.Context) error {
	if s.stop != nil {
		return s.stop(ctx)
	}
	return nil
}

type firstService struct{ testHooks }
type secondService struct{ testHooks }
type thirdService struct{ testHooks }
type startupOnlyService struct{ called bool }

func (s *startupOnlyService) Startup(context.Context) error { s.called = true; return nil }

type shutdownOnlyService struct{ called bool }

func (s *shutdownOnlyService) Shutdown(context.Context) error { s.called = true; return nil }

func TestServiceRegistration(t *testing.T) {
	a := testApp(t)
	for _, value := range []any{nil, (*rpcOnlyService)(nil), rpcOnlyService{}, &struct{}{}, &badRPCService{}, &badHookService{}, &pointerRequestService{}} {
		if err := a.Register(value); err == nil {
			t.Errorf("accepted %T", value)
		}
	}
	if len(a.services) != 0 {
		t.Fatal("failed registration changed registry")
	}
	if err := a.Register(&rpcOnlyService{}); err != nil {
		t.Fatal(err)
	}
	if err := a.Register(&rpcOnlyService{}); err == nil {
		t.Fatal("accepted duplicate name")
	}
	startup, shutdown := &startupOnlyService{}, &shutdownOnlyService{}
	if err := a.Register(startup); err != nil {
		t.Fatal(err)
	}
	if err := a.Register(shutdown); err != nil {
		t.Fatal(err)
	}
	if startup.called || shutdown.called {
		t.Fatal("registration invoked hooks")
	}
	cancel, done := runTestApp(t, a)
	connectTestApp(t, a)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !startup.called || !shutdown.called {
		t.Fatal("optional hook was not invoked")
	}
	if err := a.Register(&firstService{}); err == nil {
		t.Fatal("registration reopened after Run")
	}
}

func TestServicesStartBeforeReady(t *testing.T) {
	a := testApp(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var initialized atomic.Bool
	s := &firstService{testHooks{start: func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			initialized.Store(true)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}}
	if err := a.Register(s); err != nil {
		t.Fatal(err)
	}
	cancel, done := runTestApp(t, a)
	<-entered
	probeCtx, endProbe := context.WithTimeout(t.Context(), 100*time.Millisecond)
	conn, _, probeErr := connectLocal(probeCtx, a.endpoint, initializeParams{a.options.AppID, a.options.InstanceID, protocolVersion, "test"})
	endProbe()
	if probeErr == nil {
		_ = conn.CloseNow()
		t.Fatal("Ready before Startup completed")
	}
	if err := a.Register(&secondService{}); err == nil {
		t.Fatal("registered during Run")
	}
	if err := a.Run(t.Context()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("concurrent Run = %v", err)
	}
	close(release)
	connectTestApp(t, a)
	if !initialized.Load() {
		t.Fatal("Ready before Startup completed")
	}
	cancel()
	<-done
}

func TestStartupFailureUnwindsServices(t *testing.T) {
	a := testApp(t)
	var events []string
	failure, cleanupFailure := errors.New("startup failed"), errors.New("cleanup failed")
	hooks := func(name string, startErr, stopErr error) testHooks {
		return testHooks{
			start: func(context.Context) error { events = append(events, "start "+name); return startErr },
			stop: func(ctx context.Context) error {
				if ctx.Err() != nil {
					t.Error("cleanup received canceled context")
				}
				events = append(events, "stop "+name)
				return stopErr
			},
		}
	}
	for _, s := range []any{&firstService{hooks("first", nil, nil)}, &secondService{hooks("second", nil, cleanupFailure)}, &thirdService{hooks("third", failure, nil)}} {
		if err := a.Register(s); err != nil {
			t.Fatal(err)
		}
	}
	err := a.Run(t.Context())
	if !errors.Is(err, failure) || !errors.Is(err, cleanupFailure) {
		t.Fatalf("Run = %v", err)
	}
	want := []string{"start first", "start second", "start third", "stop second", "stop first"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("hooks = %v", events)
	}
	if _, err := readExecution(a.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("execution record remained: %v", err)
	}
	unlock, err := lockInstance(a.directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownHooksUseFreshContextInReverseOrder(t *testing.T) {
	a := testApp(t)
	var events []string
	hooks := func(name string) testHooks {
		return testHooks{stop: func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Error("shutdown context already canceled")
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Error("shutdown has no deadline")
			}
			events = append(events, name)
			return nil
		}}
	}
	if err := a.Register(&firstService{hooks("first")}); err != nil {
		t.Fatal(err)
	}
	if err := a.Register(&secondService{hooks("second")}); err != nil {
		t.Fatal(err)
	}
	cancel, done := runTestApp(t, a)
	connectTestApp(t, a)
	cancel()
	<-done
	if !reflect.DeepEqual(events, []string{"second", "first"}) {
		t.Fatalf("shutdown = %v", events)
	}
}

func TestShutdownHookDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		called := false
		services := []registeredService{
			{"first", &firstService{testHooks{stop: func(context.Context) error { called = true; return nil }}}},
			{"second", &secondService{testHooks{stop: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}}},
		}
		if err := shutdownServices(ctx, services); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown = %v", err)
		}
		if !called {
			t.Fatal("deadline skipped remaining cleanup")
		}
	})
}
