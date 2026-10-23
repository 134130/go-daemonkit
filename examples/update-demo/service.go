package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	daemon "github.com/134130/go-daemonkit"
	"github.com/134130/go-daemonkit/examples/update-demo/api"
)

type demoService struct {
	version string
	ClockService
}

func (s *demoService) Info(context.Context, api.Empty) (api.InfoResponse, error) {
	return api.InfoResponse{Version: s.version, PID: os.Getpid()}, nil
}
func (s *demoService) Now(context.Context, api.Empty) (api.NowResponse, error) {
	return api.NowResponse{Version: s.version, Time: time.Now()}, nil
}
func (s *demoService) Greet(_ context.Context, request api.GreetRequest) (api.GreetResponse, error) {
	return api.GreetResponse{Version: s.version, Message: fmt.Sprintf("Hello, %s! This RPC was added in v0.0.2.", request.Name)}, nil
}
func runDaemon(ctx context.Context, options daemon.Options) error {
	app, err := daemon.New(options)
	if err != nil {
		return err
	}
	service := &demoService{version: options.Version, ClockService: ClockService{publish: func(tick api.NowResponse) error {
		tick.Version = options.Version
		return app.PublishTopic(api.Tick, tick)
	}}}
	if err := app.Register(service); err != nil {
		return err
	}
	if err := app.Handle(api.Info, service.Info); err != nil {
		return err
	}
	if err := app.Handle(api.Now, service.Now); err != nil {
		return err
	}
	if options.Version == "0.0.2" || options.Version == "0.0.3" {
		if err := app.Handle(api.Greet, service.Greet); err != nil {
			return err
		}
	}
	if options.Version == "0.0.3" {
		if err := app.Handle(api.Runtime, service.Runtime); err != nil {
			return err
		}
	}
	err = app.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (*demoService) Runtime(context.Context, api.Empty) (api.RuntimeResponse, error) {
	return api.RuntimeResponse{GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}, nil
}
