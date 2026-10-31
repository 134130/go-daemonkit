package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	daemon "github.com/134130/go-daemonkit"
	"github.com/134130/go-daemonkit/config"
	"github.com/134130/go-daemonkit/examples/update-demo/api"
)

type demoConfig struct {
	Daemon struct {
		Greeting string `toml:"greeting"`
	} `toml:"daemon"`
}

func configFile(options daemon.Options, path string) (*config.File[demoConfig], error) {
	return config.New(config.Options[demoConfig]{AppID: options.AppID, Path: path,
		Defaults: func() demoConfig { var c demoConfig; c.Daemon.Greeting = "Hello"; return c },
		Validate: func(_ context.Context, c demoConfig) error {
			if strings.TrimSpace(c.Daemon.Greeting) == "" || len(c.Daemon.Greeting) > 80 {
				return errors.New("daemon.greeting must contain 1–80 bytes")
			}
			return nil
		},
	})
}

func reloadConfig(ctx context.Context, options daemon.Options, path string, out io.Writer) error {
	client, err := daemon.Connect(ctx, options)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	result, err := client.Invoke(ctx, api.ReloadConfig, api.ReloadRequest{Path: path})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Applied revision %d\n", result.Revision)
	return err
}
