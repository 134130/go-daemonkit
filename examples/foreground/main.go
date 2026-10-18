package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	daemon "github.com/134130/go-daemonkit"
)

func main() {
	options := daemon.Options{AppID: "example", Version: "dev"}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) == 2 && os.Args[1] == "now" {
		client, err := daemon.Connect(ctx, options)
		if err != nil {
			log.Fatal(err)
		}
		defer client.Close()
		var response NowResponse
		if err := client.Call(ctx, "ClockService.Now", NowRequest{}, &response); err != nil {
			log.Fatal(err)
		}
		log.Print(response.Time)
		return
	}
	if len(os.Args) == 2 && (os.Args[1] == "start" || os.Args[1] == "stop" || os.Args[1] == "restart") {
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
		}
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && (len(os.Args) != 2 || os.Args[1] != "daemon") {
		log.Fatal("usage: foreground [start|stop|restart|now|daemon]")
	}
	app, err := daemon.New(options)
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Register(&ClockService{}); err != nil {
		log.Fatal(err)
	}
	if err := app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
