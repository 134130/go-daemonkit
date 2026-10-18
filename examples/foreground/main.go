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
	app, err := daemon.New(daemon.Options{AppID: "example", Version: "dev"})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
