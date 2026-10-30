package config_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	daemon "github.com/134130/go-daemonkit"
	"github.com/134130/go-daemonkit/config"
)

func ExampleNewService() {
	type Settings struct {
		Greeting string `toml:"greeting"`
	}
	dir, err := os.MkdirTemp("", "config-example-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file, err := config.New(config.Options[Settings]{Path: filepath.Join(dir, "config.toml")})
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	if err := file.Save(ctx, []byte("greeting = 'hello' # preserved\n")); err != nil {
		panic(err)
	}
	var active atomic.Pointer[Settings]
	service, err := config.NewService(file, func(ctx context.Context, next Settings) (config.Prepared, error) {
		return config.Prepared{Commit: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			active.Store(&next)
			return nil
		}}, nil
	})
	if err != nil {
		panic(err)
	}
	app, err := daemon.New(daemon.Options{AppID: "config-example", Version: "dev"})
	if err != nil {
		panic(err)
	}
	if err := app.Register(service); err != nil {
		panic(err)
	}
	// App.Run normally calls these hooks; invoke them directly for this example.
	if err := service.Startup(ctx); err != nil {
		panic(err)
	}
	if err := file.Set(ctx, "greeting", `"hi"`); err != nil {
		panic(err)
	}
	fmt.Println(active.Load().Greeting)
	if err := service.Reload(ctx); err != nil {
		panic(err)
	}
	fmt.Println(active.Load().Greeting, service.Status().Revision)
	if err := service.Shutdown(ctx); err != nil {
		panic(err)
	}
	// Output:
	// hello
	// hi 2
}
