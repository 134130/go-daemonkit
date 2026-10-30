package main

import (
	"context"
	"fmt"
	"github.com/134130/go-daemonkit/config"
	"log"
	"runtime"
	"sync/atomic"
	"time"

	daemon "github.com/134130/go-daemonkit"
	"github.com/134130/go-daemonkit/examples/update-demo/api"
)

type demoService struct {
	version       string
	configuration atomic.Pointer[demoConfig]
	ClockService
}

func (s *demoService) Now(context.Context, api.Empty) (api.NowResponse, error) {
	return api.NowResponse{Version: s.version, Time: time.Now()}, nil
}
func (s *demoService) Greet(_ context.Context, request api.GreetRequest) (api.GreetResponse, error) {
	return api.GreetResponse{Version: s.version, Message: fmt.Sprintf("%s, %s! This RPC was added in v0.0.2.", s.configuration.Load().Daemon.Greeting, request.Name)}, nil
}
func runDaemon(ctx context.Context, options daemon.Options, configPath string) error {
	app, err := daemon.New(options)
	if err != nil {
		return err
	}
	service := &demoService{version: options.Version, ClockService: ClockService{logger: log.New(app.LogWriter(), "", log.LstdFlags), publish: func(tick api.NowResponse) error {
		tick.Version = options.Version
		return app.PublishTopic(api.Tick, tick)
	}}}
	file, err := configFile(options, configPath)
	if err != nil {
		return err
	}
	configuration, err := config.NewService(file, func(ctx context.Context, next demoConfig) (config.Prepared, error) {
		return config.Prepared{Commit: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			service.configuration.Store(&next)
			return nil
		}}, nil
	})
	if err != nil {
		return err
	}
	if err := app.Register(configuration); err != nil {
		return err
	}
	if err := app.Handle(api.ReloadConfig, func(ctx context.Context, request api.ReloadRequest) (api.ReloadResponse, error) {
		if request.Path != file.Path() {
			return api.ReloadResponse{}, fmt.Errorf("CLI config path differs from the running daemon configuration")
		}
		err := configuration.Reload(ctx)
		return api.ReloadResponse{Revision: configuration.Status().Revision}, err
	}); err != nil {
		return err
	}
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
