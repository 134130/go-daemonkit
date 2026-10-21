package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	daemon "github.com/134130/go-daemonkit"
	"github.com/134130/go-daemonkit/examples/foreground/clockapi"
)

func main() {
	options := daemon.Options{AppID: "example", Version: "dev"}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) == 2 && (os.Args[1] == "now" || os.Args[1] == "now-raw" || os.Args[1] == "watch") {
		client, err := daemon.Connect(ctx, options)
		if err != nil {
			log.Fatal(err)
		}
		defer client.Close()
		if os.Args[1] == "watch" {
			sub, err := client.SubscribeTopic(ctx, clockapi.Tick)
			if err != nil {
				log.Fatal(err)
			}
			defer sub.Close()
			for {
				tick, err := sub.Next(ctx)
				if err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					log.Print(err)
					return
				}
				log.Print(tick.Time)
			}
		}
		var response clockapi.NowResponse
		if os.Args[1] == "now-raw" {
			err = client.Call(ctx, "clock.now", clockapi.NowRequest{}, &response)
		} else {
			response, err = client.Invoke(ctx, clockapi.Now, clockapi.NowRequest{})
		}
		if err != nil {
			log.Fatal(err)
		}
		log.Print(response.Time)
		return
	}
	if (len(os.Args) == 2 && (os.Args[1] == "start" || os.Args[1] == "stop" || os.Args[1] == "restart")) ||
		(len(os.Args) == 3 && os.Args[1] == "update") {
		manager, err := daemon.NewManager(options, "daemon")
		if err != nil {
			log.Fatal(err)
		}
		switch os.Args[1] {
		case "start":
			err = manager.EnsureRunning(ctx)
		case "stop":
			err = manager.Stop(ctx)
		case "restart":
			err = manager.Restart(ctx)
		case "update":
			err = manager.Update(ctx, os.Args[2])
		}
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && (len(os.Args) != 2 || os.Args[1] != "daemon") {
		log.Fatal("usage: foreground [start|stop|restart|now|now-raw|watch|daemon] or foreground update <verified-executable>")
	}
	app, err := daemon.New(options)
	if err != nil {
		log.Fatal(err)
	}
	service := &ClockService{publish: func(tick clockapi.NowResponse) error { return app.PublishTopic(clockapi.Tick, tick) }}
	if err := app.Register(service); err != nil {
		log.Fatal(err)
	}
	if err := app.Handle(clockapi.Now, service.Now); err != nil {
		log.Fatal(err)
	}
	if err := app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
