package main

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"time"

	daemon "github.com/134130/go-daemonkit"
	"github.com/134130/go-daemonkit/examples/update-demo/api"
)

type demoService struct {
	version string
	ClockService
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
	service := &demoService{version: options.Version, ClockService: ClockService{logger: log.New(app.LogWriter(), "", log.LstdFlags), publish: func(tick api.NowResponse) error {
		tick.Version = options.Version
		return app.PublishTopic(api.Tick, tick)
	}}}
	if err := app.Register(service); err != nil {
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
	return app.Run(ctx)
}

func (*demoService) Runtime(context.Context, api.Empty) (api.RuntimeResponse, error) {
	return api.RuntimeResponse{GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}, nil
}
