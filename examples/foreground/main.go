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
	if len(os.Args) == 2 && os.Args[1] == "start" {
		manager, err := daemon.NewManager(options, "daemon")
		if err != nil {
			log.Fatal(err)
		}
		if err := manager.EnsureRunning(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && (len(os.Args) != 2 || os.Args[1] != "daemon") {
		log.Fatal("usage: foreground [start|daemon]")
	}
	app, err := daemon.New(options)
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
